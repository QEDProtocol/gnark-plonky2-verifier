# MSM GPU 实验接入

用户已授权在腾出的 5070 Ti 上测试。小向量/不同基点大向量与一份真实 deposit 验证通过；三类重复对照已完成，各27份全部验证通过，Go Prove 初步耗时降低33%–38%。
原 CPU 版本的 63 次验证不能用来证明本次新代码正确。
完整 CUDA 后端、Go 主程序和测试二进制已编译并实际运行通过；SM120 单卡隔离与GPU显存观测已验证。

## 第一阶段范围

保留线上 gnark fork `2081880d08df`、gnark-crypto、R1CS/PK/VK 和 CPU verifier，
通过本地补丁在原 BN254 prover 中引入 `ProveWithMSM`。只替换四次 G1 MSM
（A、B、K、Z）和一次 G2 MSM（B2）。solver、FFT、commitment 和证明格式沿用原代码。
后续可选 `--gpu-h` 额外替换 computeH，详见文末；默认配置仍仅替换 MSM。
不使用旧 fork 已失配的 `icicle` build tag，不升级整套 gnark，也不重新 Setup 生产参数。

三个实验构建：

| 后端 | 用途 | 构建依赖 | 当前运行状态 |
|---|---|---|---|
| cpu | 原始 CPU 对照 | 原 go.mod/go.sum | 旧版验证通过；本轮未运行 |
| cpu-msm | 先验证新增接口与生命周期，仍使用 CPU MultiExp | lab-msm.mod、本地 gnark 补丁 | 接口测试通过；同二进制CPU对照27份通过 |
| icicle-msm | 同一接口替换为 ICICLE G1/G2 MSM | lab-gpu.mod、ICICLE v3.2.2 | 向量测试、单卡隔离与三类真实27份验证通过 |

ICICLE 固定 commit `b62bbbe518a73214da10ece26969ad55e6fa0cd0`。
`gnark-source.json` 固定源码指纹，补丁保存在 `gnark-msm.patch`、`gnark-h-timing.patch`、`icicle-runtime.patch`、`icicle-cuda-build.patch`、
`icicle-msm-stream-dependency.patch` 与 `icicle-msm-chunk-cleanup.patch`。
完整 H 扩展另外使用 `gnark-h-backend.patch`、`icicle-ntt-domain-release.patch`、`icicle-ntt-coset-gpu.patch`。
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

## 后续阶段定位与 NTT 兼容性

新增 `compute_h` 内部计时，以及每次 GPU MSM 的 `msm_detail`：

- `wait_ms` 是等待 engine mutex 的时间，各任务的等待可能重叠，不能相加当作关键路径。
- `exclusive_ms` 是拿锁后的工作时间；五次 MSM 串行执行，可以相加估算 MSM 总工作时间。
- `backend_ms` 是同步 ICICLE 调用之和，包含拷贝、转换和执行，不是纯 kernel 时间。
- `host_overhead_ms = exclusive_ms - backend_ms`；日志写入本身不包含在 exclusive 计时中。
- `compute_h`、MSM 都嵌套在 `prover_compute`，后者嵌套在 `groth16_prove`，不能重复计入总耗时。

新建依赖镜像时 `prepare` 会依次应用两个 gnark 补丁。已有旧镜像需要先在仓库根目录执行
`patch --batch --forward -d lab-deps/gnark -p1 < dev/proverbench/gnark-h-timing.patch`，再 build；
已应用该补丁的镜像不要重复执行。构建会核验完整依赖指纹。两个补丁已从原始模块重放并逐树校验一致。

`gpu-c4-stage-profile-20260924` 完成18份真实证明（9预热、9测量），全部通过原 CPU verifier。
本轮出现其他重型 CPU 任务，因此仅用于阶段定位，不作为新的速度对照。
CPU profile 在参数加载之后启动，包含预热；FFT 路径下的前15项热点占总 CPU 采样的80.23%，
主要为有限域乘法和蝶形运算。这是 CPU 采样占比，不能直接解释为墙钟时间占比或预计加速比。

```sh
python3 dev/proverbench/analyze_gpu_stages.py lab-private/gpu-c4-stage-profile-20260924

# 独立小规模 NTT 兼容性检查，不加载真实证明参数；运行前先 build。
python3 dev/proverbench/gpu_runtime.py --name gpu-ntt-next \
  --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d --ntt
```

`gpu-ntt-compat-threaded-20260924` 的12项检查全部通过：长度16、1024、65536，
正/逆变换分别覆盖普通域和 coset，逐元素与原 gnark FFT 相同。
使用原 gnark 的根、显式 canonical 标量转换与自然序输出比较；每个测试 goroutine 锁定 OS 线程并选择设备。
该测试没有接入真实 prover。后续先实现完整 computeH 对照，保留补零、七次变换、
逐点运算和最终 bit-reversed 顺序，再验证生产规模域、显存生命周期及原 CPU Verify。
公平性能回放等待 CPU 空闲；参数加载优化仍单独保留为待办。

## 完整 GPU computeH（可选，已验证）

`--backend icicle-msm --gpu-h` 保留4块MSM，同时替换完整 computeH；不加 `--gpu-h` 即为原 CPU H 对照。
solver、原参数、CPU verifier和证明格式不变。当前只支持2至8M二次幂域，超范围报错；没有CPU回退。
三个设备向量、一个复用host缓冲区，七次NTT与逐点计算保持在设备上；host编码转换最多8线程。
使用原根、coset、补零和最终bit-reversed顺序，每次调用结束释放设备向量与domain，不缓存GPU key/domain。
引擎和domain生命周期分别加锁，锁定OS线程后选卡；CUDA调用同步，分配/传输/释放错误返回。
prover 在 H 失败或输出长度错误时拒绝继续，返回前等待 H 与 witness 过滤任务收尾。

固定ICICLE版本另外修复两处：

- domain release 原先把 `cudaMallocManaged` 的 twiddles 指针直接清空，现调用 `cudaFree` 并等待流释放完成。
  修复前1M域5次循环使可用显存较首次减少128MiB；仅释放补丁后原测试通过，延长到21次也通过。
  这是新GPU路径的漏洞，与此前CPU/FFI内存问题不同。驱动记账有波动，不能要求每次读数完全一致。
- 任意coset幂表原先逐元素在CPU生成再上传，现在在NTT同一流的CUDA kernel内计算。
  4M的NTT总时间从约1.18秒降至40毫秒；加入并行host转换后完整H约0.24秒，8M约0.49秒。
  这些是8线程合成检查的局部时间，不等于完整证明加速比。

从 `45344c3` 的既有依赖镜像升级时，按顺序执行以下命令；已应用的补丁不要重复执行。
新镜像由 `prepare --gpu --fetch` 自动按顺序应用全部补丁，构建严格校验源码树指纹。

```sh
patch --batch --forward -d lab-deps/gnark -p1 < dev/proverbench/gnark-h-backend.patch
patch --batch --forward -d lab-deps/icicle-gnark -p1 < dev/proverbench/icicle-ntt-domain-release.patch
patch --batch --forward -d lab-deps/icicle-gnark -p1 < dev/proverbench/icicle-ntt-coset-gpu.patch
python3 dev/proverbench/gpu_build.py build --variant icicle-msm --cuda --compile-tests

# 每次使用新的目录名。以下命令会执行计算：
python3 dev/proverbench/gpu_runtime.py --name h-small-next --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d --h
python3 dev/proverbench/gpu_runtime.py --name h-large-next --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d --h-large
python3 dev/proverbench/gpu_runtime.py --name h-domain-next --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d --domain-memory
python3 dev/proverbench/lab.py run --name h-real-next --backend icicle-msm --gpu-h \
  --gpu-uuid GPU-08b12be2-292f-d468-a175-cae71edd7f8d --scenario all --warmup 1 --cycles 1 --gomaxprocs 32
```

验证：12项小规模完整H、4M→8M→4M逐元素对照、独立12项NTT、错误后恢复、H接口失败拒绝/任务收尾、MSM回归均通过。
BAAB 72份真实证明全部通过，每配置每场景6个正式样本：bridge 3.920→2.219秒（−43.4%），
deposit 2.213→1.466秒（−33.8%），withdrawal 2.315→1.622秒（−30.0%）。
仅切换gpu_h；后台CPU粗估1.35–2.22核，不称为绝对干净的生产基线，不与之前不同实验收益相乘。

随后补强部分domain初始化失败、coset kernel启动失败的清理；最终二进制再完成36份间隔2秒的混合回放，全部通过。
35个空闲窗口进程显存均230MiB，整卡空闲采样386–454MiB；进程/整卡采样峰值分别1286/1764MiB。
RSS高水位16.137GiB，后段回落，Go堆有GC下降，swap=0。NVML进程/整卡查询不是同时进行，不能相减或当精确峰值。
有限回放没有出现空闲显存累计增长，不能排除所有长期泄漏。GPU H当前仍须显式启用，未接入Rust FFI或部署。

配对二进制及库保留在各run目录和 `lab-private/gpu-h-paired-libraries-20260924`；最终混合版本指纹单独记录。
数值报告可在仓库根目录复现：

```sh
python3 dev/proverbench/analyze_gpu_h.py
python3 dev/proverbench/analyze_gpu_h_memory.py
```

命令固定读取本次实验目录；原始证据位于 `lab-private/gpu-h-*` 与 `gpu-ntt-domain-*`。
参数加载约100秒继续独立保留为待办。
