# MSM GPU 实验接入

用户已授权在腾出的 5070 Ti 上测试。小向量/不同基点大向量与一份真实 deposit 验证通过；三类重复对照已完成，各27份全部验证通过，Go Prove 初步耗时降低33%–38%。
原 CPU 版本的 63 次验证不能用来证明本次新代码正确。
完整 CUDA 后端、Go 主程序和测试二进制已编译并实际运行通过；SM120 单卡隔离与GPU显存观测已验证。

## 第一阶段范围

保留线上 gnark fork `2081880d08df`、gnark-crypto、R1CS/PK/VK 和 CPU verifier，
通过本地补丁在原 BN254 prover 中引入 `ProveWithMSM`。只替换四次 G1 MSM
（A、B、K、Z）和一次 G2 MSM（B2）。solver、FFT、commitment 和证明格式沿用原代码。
不使用旧 fork 已失配的 `icicle` build tag，不升级整套 gnark，也不重新 Setup 生产参数。

三个实验构建：

| 后端 | 用途 | 构建依赖 | 当前运行状态 |
|---|---|---|---|
| cpu | 原始 CPU 对照 | 原 go.mod/go.sum | 旧版验证通过；本轮未运行 |
| cpu-msm | 先验证新增接口与生命周期，仍使用 CPU MultiExp | lab-msm.mod、本地 gnark 补丁 | 接口测试通过；同二进制CPU对照27份通过 |
| icicle-msm | 同一接口替换为 ICICLE G1/G2 MSM | lab-gpu.mod、ICICLE v3.2.2 | 向量测试、单卡隔离与三类真实27份验证通过 |

ICICLE 固定 commit `b62bbbe518a73214da10ece26969ad55e6fa0cd0`。
`gnark-source.json` 固定源码指纹，补丁保存在 `gnark-msm.patch`、`icicle-runtime.patch`、`icicle-cuda-build.patch`、
`icicle-msm-stream-dependency.patch` 与 `icicle-msm-chunk-cleanup.patch`。
runtime 补丁释放 `LoadBackend` 的临时 `C.CString`，避免实验接入引入新的字符串泄漏。
镜像位于忽略目录 `lab-deps/`，构建仍检查其完整源码树指纹，不能绕过源码版本检查。
生产 worker、Rust FFI、主 go.mod/go.sum 均不修改。

## 编译命令：不执行测试或证明

```sh
# 复用已有模块缓存；首次缺少 ICICLE 时显式下载固定源码：
python3 dev/proverbench/gpu_build.py prepare --gpu --fetch

# 纯 CPU 接口对照；go test -c 只产生测试二进制：
python3 dev/proverbench/gpu_build.py build --variant cpu-msm --compile-tests

# GPU 适配代码及真实 ICICLE 调度库的编译/链接检查：
python3 dev/proverbench/gpu_build.py build --variant icicle-msm --compile-tests

# 完整 CUDA SM120 内核 + Go 主程序/测试二进制，只编译：
python3 dev/proverbench/gpu_build.py build --variant icicle-msm --cuda --compile-tests
```

构建使用 Go 1.22.3、nice 19、单编译进程、独立缓存。
默认只构建调度库到 `lab-deps/icicle-dispatch`；加 `--cuda` 才构建完整 CUDA 后端，安装到
`lab-deps/icicle-cuda-sm120`，二进制与构建清单位于 `lab-bin/icicle-cuda/`。
本机 CUDA 13.4 + GCC 14，显式指定 SM120（两张卡实测 compute capability 12.0），避免 `native` 自动探测。
CPU fallback 和库自带测试都关闭。补丁将 nvcc `--split-compile 0` 改为 `1`，避免单个 nvcc 占满 CPU。
脚本固定 C/C++ 编译器，并在构建前核验 CMake cache 中的安装路径和后端开关，防止上游重置参数。
构建记录分别写入 `lab-bin/cpu-msm/build.json` 与 `lab-bin/icicle-msm/build.json`。
默认 CPU 仍由 `lab.py build --compile-tests` 构建。

以后 CPU 接口对照可用 `lab.py run --backend cpu-msm`，复用只读、断网隔离与原参数校验。
用户已授权测试；run 会启动真实证明负载。`lab.py run --backend icicle-msm` 已接入完整 CUDA 构建，要求显式提供 `--gpu-uuid`；
先核验完整源码树、二进制和共享库哈希，再映射单卡。源码/库变化或只有调度库时拒绝启动。
不要绕过 runner 直接启动 GPU 二进制作为正式基线。

## 设备、内存和计时

- 单个 CUDA 设备，明确指定 device ID 与绝对 backend-dir；设备不存在或运行失败直接报错，不回退 CPU。
- 锁定 OS 线程后选择设备，同一 engine 的 MSM 串行执行，使用同步调用；没有异步输入存活期悬空问题。
- 默认每块 1,048,576 个点（也是当前上限）；边界块长度可不同，空输入直接得到无穷远点。
- 上述为 Go 外层窗口；`--msm-internal-chunks 1|2|4|8` 控制每个窗口内 ICICLE 的流水线块数，默认 4。
- 直接传入同布局的小端 Montgomery 数组，显式设置 ICICLE 输入格式，避免完整 key 的额外转换副本。
- 将 ICICLE 齐次坐标正确转换为 gnark Jacobian 坐标，并检查输出在曲线上；最终仍需原 CPU verifier 验证。
- 分块限制输入规模，不代表已证明显存峰值有某个上界。CUDA scratch 和驱动缓存要在实测时观测。
- 本版不保留 GPU key；可能增加传输开销，先验证正确性再决定是否驻留 key 或迁移 FFT/NTT。
- gnark 所有 MSM 任务结束后 Prove 才返回，含错误路径；调用方不会提前关闭 engine。
- `msm_g1_A/B/K/Z`、`msm_g2_B2` 记录调用耗时和点数。耗时包含等待 engine 锁、传输与结果转换，
  不是纯 kernel 时间；多个调用区间重叠，不能相加当作 Prove 耗时，也不能与 solver/prover_compute 重复相加。

## 空闲窗口恢复后的验证顺序

1. 执行框架测试与新 CPU MSM 接口测试；测试包含原 CPU verifier、错误 public input 拒绝、错误路径任务收尾。
2. 原 CPU 与 cpu-msm 分别验证三类真实样本，确认既有参数兼容，再建立干净 CPU 基线。
3. CUDA/Blackwell 构建和单卡映射代码已补齐；先验证实际运行中的隔离、库加载和显存指标。
4. 执行坐标转换测试，然后用 GPU 小向量对照 CPU：G1/G2、零标量、无穷远、空输入、不整除分块、多块聚合、错误设备。
5. 三类真实样本逐一经原 CPU verifier 验证；之后才进行同条件重复性能测试和长时内存观测。
6. 依据实测再决定分块大小、key 驻留、多 GPU、NTT 等后续优化。参数加载优化保持为单独待办。

重复 CPU/GPU 对照完成前不提供加速比；短回放也不提供长期显存上界或可上线结论。

## 单卡测试准备与命令（已执行，重跑需新实验名）

编译无需腾显卡。运行时建议先空出 5070 Ti，尽量停止该卡上的其他计算任务；不用动另一张卡。
本次只读查询：5060 Ti（16 GiB）使用约 12.2 GiB，5070 Ti（16 GiB）使用约 8.7 GiB；
这些是查询时的整卡占用，不是本实验的用量。是否需要更大显存，要由分块与真实证明实测确认。
正式速度对比还需要 CPU 无其他重型证明/编译任务。

以下命令都会执行 GPU 工作负载，应在恢复测试后执行：

```sh
# 首先是合成小向量，无真实输入、无参数加载：
python3 dev/proverbench/gpu_runtime.py --name gpu-vector-check-01 \
  --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d

# 小向量通过后，单份真实 deposit，较保守的 65536 点分块：
python3 dev/proverbench/lab.py run --name gpu-deposit-smoke-01 \
  --backend icicle-msm --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d \
  --sample deposit-00 --warmup 0 --cycles 1 --gomaxprocs 8 --msm-chunk-size 65536
```

合成测试覆盖 G1/G2 CPU 对照、零标量、无穷远、空数组、小数组、分块余数、多块聚合、关闭后拒绝调用。
测试二进制也校验哈希。未设置 `PROVERBENCH_GPU_TEST=1` 时 GPU 测试跳过，不会自动探测设备。
运行脚本设置 UUID 可见性后，进程内 ordinal 固定为 0，不等于主机 `nvidia-smi` 的编号。
只绑定所选 `/dev/nvidiaN` 和 CUDA 公共控制/UVM 节点；不绑定另一张卡，不关闭他人进程。
本轮已验证驱动在此单卡隔离环境中可用。

真实回放增加 `gpu.jsonl`：默认每 2 秒记录 NVML 的整卡显存、利用率、温度、功率与该证明进程的显存。
`summary.json` 分别统计进程/整卡的采样峰值，标注其他计算进程；采样会漏掉短峰值，不能称为严格峰值。
不记录显存内容。GPU 运行的 `run.json` 记录 UUID、驱动、架构、分块大小和库构建指纹。

## 已发现的运行兼容问题

固定版本 ICICLE 的内部多分块缺少跨 stream 依赖：后续块的点在 producer stream 异步拷贝，
`upload_points` 却另开 stream 做 Montgomery 转换，没有等待输入就绪。大向量更容易进入该路径，
是否失败仍受调度时序影响，不能解释为固定长度上限。
400项CPU/GPU对照中原版21项失败、普通编码点200项全通过；只补 event 依赖后400项全通过。
另一个独立补丁修复中间块 `return_buckets` 提前返回遗漏辅助 stream/event 清理；两补丁合用400项仍全通过。
内部4块的三类真实样本在262144/1048576两个外层窗口各9份、共18份全部通过原CPU verifier。没有改数学算法、参数或使用CPU回退。
当前实验默认使用外层1048576/内部4；可显式指定262144/1复现此前配置。不将本次结果泛化为所有ICICLE版本。

```sh
# 400项正确性矩阵：点编码、G1/G2、5种规模和1/2/4/8/自动块数。
python3 dev/proverbench/gpu_runtime.py --name gpu-pipeline-check-next \
  --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d --pipeline

# 262144/1048576点，轮换1/2/4/8块顺序，每配置1次预热+8次计时，逐次与CPU校验。
python3 dev/proverbench/gpu_runtime.py --name gpu-chunk-timing-next \
  --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d --chunk-timing
```

两个扩展测试需要显式开关，普通合成测试入口会跳过。实验记录和分块性能结论见
`lab-private/gpu-pipeline-analysis-20260924/REPORT.md`。

公平 CPU 对照使用 `lab.py run --backend cpu-msm --cpu-control`，与 GPU 使用同一 CUDA 链接二进制；
CPU 对照不绑定 GPU 设备，参数/CPU verifier/接口计时不变。两边均需相同 GOMAXPROCS、样本、预热和轮次。

首轮实测详见根目录 WORKLOG.md 与 lab-private/gpu-first-comparison-20260924/REPORT.md。该首轮对照使用内部1块；后续多分块修复没有改写历史数据。正式性能与长期内存结论仍需后续验证。

## 4块实验默认配置

当前Go程序和Python运行入口默认使用1048576点窗口/内部4块。
engine未显式指定内部块数时也取4，外层窗口仍由调用方指定。
只改变实验默认选择；仍固定gnark参数和CPU verifier，构建仍要求已有两项CUDA同步/清理补丁。

```sh
# 默认4块；以下命令会执行真实证明，结果目录名必须未使用。
python3 dev/proverbench/lab.py run --name gpu-c4-next \
  --backend icicle-msm --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d \
  --scenario all --warmup 1 --cycles 2 --gomaxprocs 32 --gpu-sample-interval 0.5

# 若需复现旧实验配置，在上述命令追加：
# --msm-chunk-size 262144 --msm-internal-chunks 1
```

ABBA配对及混合重复运行证据见`lab-private/gpu-c4-paired-analysis-20260924/`；
两个调参维度同时改变，不能将全部完整证明收益归因于内部块数。参数加载优化继续独立处理。

本轮ABBA配对72份与新默认混合54份合计126份全部通过CPU verifier。Go Prove中位数较旧GPU配置降低8.1%–11.2%；有限混合回放中空闲进程显存均228MiB、RSS随GC有回落。详细计时口径、显存采样限制和证据见上述目录的REPORT.md。
