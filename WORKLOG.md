# Go Groth16 调优工作文档

更新日期：2026-09-24（Asia/Kuala_Lumpur，UTC+08）。

## 当前任务与约束

目标：建立独立、可复现的 Go Groth16 测试环境，用现有线上样本建立 CPU 基线，随后验证 ICICLE GPU 接入的正确性、耗时与内存收益。

**最新授权：用户已腾出 5070 Ti 并要求继续测试。可运行隔离 GPU 正确性测试、归档真实证明和串行 CPU/GPU 对照；不改生产环境。**

- 本轮只推进 MSM/GPU 实验接口和编译。此前 CPU 测试结果保留，不能作为新代码验证结果。
- 不启动服务或定时任务，各轮串行运行并保留独立结果。
- 后续回放使用已归档数据，不触发线上请求，不修改生产服务。
- 不改写现有参数，不通过重新 Setup 绕过兼容问题；不输出证明、witness 或密钥内容。

## 工作环境

| 项目 | 当前值 |
|---|---|
| 工作目录 | `/home/peter/git/bridge_zilong/prove-proxy-go-lab-20260924` |
| 分支 | `lab/groth16-benchmark-20260924` |
| 独立 clone 来源 | `../gnark-single-solve-20260922` |
| 基础提交 | `aebd3683cd6a2bcf83113113c142344e94d05367` |
| Go 工具链 | Go 1.22.3，`../tmp/prove-proxy-replay-20260921/toolchain1223/go/bin/go` |
| gnark fork | `github.com/zilong-dai/gnark`，commit `2081880d08df` |
| 构建目录 | `lab-bin/` |
| 独立编译缓存 | `lab-cache/` |
| 未来运行结果 | `lab-private/<唯一实验名>/` |
| 当前后端 | 原 CPU、cpu-msm 接口对照、icicle-msm 实验适配；新版本仅编译 |

实验代码尚未提交或推送。原生产目录与生产 `worker.go` 未改动，`go.mod/go.sum` 沿用基础提交。
构建复用本地 module cache，不自动联网下载；新 Go 代码受 `proverbench` build tag 保护。

## 已完成

- [x] 创建独立源码环境、实验分支和输出目录约定。
- [x] 实现 Go 串行回放入口，支持场景/样本筛选、预热、重复轮次和请求间隔。
- [x] 只读加载现有 circuit/PK/VK，用现有 CPU verifier 验证每次证明，失败不重试。
- [x] 实现输入准备、witness、证明、验证、序列化和参数加载计时。
- [x] 接入 gnark 的 solver/prover_compute 数字日志，保留请求序号。
- [x] 实现 Go heap/GC、RSS/HWM/swap 与外部 `/proc` CPU/PSS/匿名内存采样代码。
- [x] 实现独立运行目录、输入 SHA256 校验、构建指纹、预热排除、结果汇总和对照工具。
- [x] 准备可选 CPU/heap profile、GOMAXPROCS/GOGC/GOMEMLIMIT 调节与显式 GC 实验入口。
- [x] 准备测试代码，覆盖筛选、统计、后端拒绝回退和小电路验证。
- [x] 主程序编译通过；`go test -c` 编译测试二进制通过。
- [x] Python AST 语法检查、`git diff --check` 通过。

**最新运行状态：框架 5 项测试通过；三类 9 份真实输入累计完成 63 次证明，全部通过现有 CPU verifier。尚未获得无重型后台任务干扰的正式性能基线。**

其中两个完整进程各完成 9 次预热和 18 次正式记录，共 36 次预热后记录；另一次完成 9 份正确性样本。
先后遇到 jemalloc 部署 smoke 回放、注册回放和 Rust 编译，并在编译退出后观察到新的 psy_user_cli。
用户随后改为暂停测试、先推进代码；不再等待协调回复，不自动启动新测试。
目前没有本实验室的证明负载在运行，不停止其他 agent 的进程。

阶段报告：[lab-private/cpu-baseline-interim/REPORT.md](lab-private/cpu-baseline-interim/REPORT.md)。
原始数据和排除原因保存在各运行目录；目录名称含 clean/idle 不代表整轮实际无干扰，以 EXCLUDED.md 和报告为准。

参考值（受背景负载干扰，不用于确认 GPU 提速）：Go Prove 中位数 bridge 7.516 秒、deposit 4.636 秒、withdrawal 4.531 秒；每场景 12 次预热后记录。
两个完整进程参数加载合计 105.64/98.78 秒，RSS 高水位 16.27/16.33 GiB，进程 swap=0；短回放不能排除长期泄漏。

此前 CPU 测试所用的一版主程序 SHA256（不是本轮新代码）：
`eb5e3dcb3ae661342ef6a3afaa0ea10c9a98b619e48c6c9634cc44ad8957b242`。
当前构建信息见 `lab-bin/build.json`、`lab-bin/cpu-msm/build.json`、`lab-bin/icicle-msm/build.json`。这些都不是生产 Rust/CArchive 二进制。

注意：当前源码指纹包含未忽略的文档。新增或编辑本文后，旧构建指纹会失效；以后运行前先重新 build。
文档更新后，下一次运行前需要刷新构建指纹；原有运行的 build 信息保持原样。

## 代码与资料导航

| 路径 | 用途 |
|---|---|
| [dev/proverbench/README.md](dev/proverbench/README.md) | 详细使用说明、命令、指标口径和限制 |
| [cmd/proverbench/main.go](cmd/proverbench/main.go) | Go 回放、后端接口、CPU 验证、阶段和内存事件 |
| [worker/benchmark_lab.go](worker/benchmark_lab.go) | 从生产输入准备逻辑提取的 assignment 构造，仅实验构建可用 |
| [dev/proverbench/lab.py](dev/proverbench/lab.py) | build/run/report/compare、文件校验、隔离与外部观测 |
| [cmd/proverbench/main_test.go](cmd/proverbench/main_test.go) | 3 项 Go 测试已执行通过 |
| [dev/proverbench/test_lab.py](dev/proverbench/test_lab.py) | 2 项 Python 测试已执行通过 |
| [GPU 调研](../tmp/prove-proxy-groth16-gpu-research-20260924.md) | ICICLE 方案、当前 fork 接口失配和工程成本 |
| [线上阶段耗时](../tmp/prove-proxy-stage-timing-20260924/REPORT.md) | 实测窗口、样本数与 GPU 收益边界 |
| [内存调查交接](../tmp/prove-proxy-system-memory-handoff-20260923/HANDOFF.md) | 内存问题背景、已有回放与观测资料 |

## 数据与测量口径

数据根目录：`../tmp/prove-proxy-replay-20260921/private/artifacts/`。
`manifest.json` 登记 bridge、deposit、withdrawal 各 3 个 FFI 样本，以及对应的现有参数文件。
run 已校验所选文件的大小和 SHA256；后续每次运行继续校验，避免使用不同参数混做对比。

- 该框架测 Go 阶段，不含 Rust Plonky2、FFI、HTTP 或链上提交。
- `solver`、`prover_compute` 是 `groth16_prove` 的内部时间，不可重复相加；新增 MSM 构建分别计五次调用，区间可能重叠且含排队/传输，不能求和充当纯 GPU 时间。
- 序列化仅覆盖原生 proof JSON，不含生产额外的 Solidity 字段和 VK JSON。
- 只加载所选场景参数；单场景与三场景运行的常驻内存不同。
- 预热排除出耗时汇总，但内存高水位包含加载和预热。
- 当前 CPU 构建禁用 cgo，不能视为生产二进制完全相同的性能基线。
- 正确性依据是既有 VK 与 public input 的验证结果，不能要求带随机性的 CPU/GPU proof 字节一致。
- 当前没有 CPU/GPU 加速比或显存实测结果。

## 后续工作顺序

用户最新要求暂停所有测试。本轮只做代码与编译，后续在空闲窗口恢复测试；当前没有定时/自动任务。

### 参数加载耗时优化（用户新增待办，2026-09-24）

- [ ] 在 CPU/GPU 证明基线完成后，单独调查参数加载启动时间。本轮保留现有加载路径，避免影响基线可比性。
- 首次真实样本检查中，bridge 加载 circuit/PK/VK 合计约 48 秒；三场景全部加载约 121 秒。详细数据见 `lab-private/cpu-smoke-p32-retry/metrics.jsonl`；这是单次观察，不是已完成的稳定统计。
- 后续分解磁盘读取、反序列化、曲线点解压/校验及分配开销；分别观察文件缓存热态和进程首次加载，不能把“新进程”直接称为“磁盘冷缓存”。
- 评估保持安全校验与参数语义的预处理格式、缓存复用或加载并行策略，同时检查启动峰值内存。不能直接跳过参数校验来换取时间。
- 验收指标：各参数加载耗时、全部就绪时间、峰值 RSS，以及原 VK 下证明验证结果；冷/热条件写入实验记录。

### CPU/GPU 证明优化主线

1. 已完成：重新编译、执行 5 项框架测试、验证隔离环境与真实输入。
2. 已完成：三类 9 份样本全部验证，两个进程完成预热和重复测量；累计 63 次证明验证成功。
3. 待空闲后恢复：三个独立进程各预热 9 次、测量 18 次，建立可用于 GPU 对比的正式 CPU 基线；继续记录整机 CPU 计数器和后台负载。
4. 检查 solver 与其后计算占比，必要时增加 NTT/MSM/数据传输的独立计时。
5. 已选择保留当前 fork/参数、只替换五次 MSM；新增接口与 ICICLE 适配已编译。接下来补齐 CUDA 计算后端构建、设备映射与显存观测。
6. 保留 CPU 验证器，用原 circuit/PK/VK 验证 GPU 输出；显存不足或证明失败必须明确失败，不能静默回退后计为 GPU 成功。
7. 再比较冷启动/热缓存、key 常驻显存开关、重复混合请求和内存稳定性；根据真实收益决定是否接入 Rust FFI。

旧 ICICLE 代码引用过时的 CosetTable/HintID 接口，本轮通过当前 CPU prover 的 MSM 接口绕开该旧路径，不升级参数格式。Rust CArchive 尚无 CUDA 链接配置，GPU 运行尚未开放，不能声称已可上线。

### 本轮代码进展（测试暂停后）

- 新增 CPU MSM 对照与 ICICLE MSM 适配，保留 solver、FFT、PK/VK 和 CPU verifier。
- 单设备、同步调用、分块 G1/G2 MSM；显式 Montgomery 输入与齐次/Jacobian 坐标转换；无 CPU 自动回退。
- 补齐 Prove 错误路径任务收尾，新增正确性/错误注入/坐标转换测试源码，未执行。
- 固定 gnark/ICICLE 源码指纹、独立 modfile 和可重放补丁；本地依赖不修改公共 module cache。
- 原 CPU、cpu-msm、icicle-msm 主程序与测试二进制编译通过。GPU 当前只链接真实 ICICLE 调度库，CUDA MSM 后端未构建、未运行。
- 构建中发现上游 CMake 更换编译器会清空缓存选项；一次安装尝试被只读系统目录拒绝，未修改系统库。已固定编译器并在 build/install 前检查实际缓存中的前缀与所有后端开关，随后在实验目录构建成功。
- 操作步骤、限制和待验证顺序见 [dev/proverbench/GPU.md](dev/proverbench/GPU.md)。参数加载优化继续留待后续。

## 工作记录

| 日期 | 动作 | 验证状态 |
|---|---|---|
| 2026-09-24 | 调研 gnark/ICICLE，核对生产兼容源码 | 源码与文档检查；无 GPU 运行 |
| 2026-09-24 | 建立独立实验环境并编写 Go 测试框架 | 主程序与测试二进制编译通过 |
| 2026-09-24 | 用户要求仅写代码、允许编译，暂停运行 | 已遵守；未执行测试、证明或回放 |
| 2026-09-24 | 建立本工作文档 | 文档变更；后续运行前需刷新构建指纹 |
| 2026-09-24 | 用户授权开始 CPU 测试 | 5 项框架测试通过；9 个真实输入累计 63 次验证通过 |
| 2026-09-24 | 用户要求参数加载优化留待后续 | 已加入待办，本轮未改变参数加载路径 |
| 2026-09-24 | 多次测量遇到其他回放/编译任务 | 已保留结果与排除原因；没有正式干净基线 |
| 2026-09-24 | 用户暂停测试，继续推进 GPU 代码 | 新增 MSM 适配与三种构建；只编译，未运行测试/证明 |

后续更新记录时分别标明“代码已写”“编译通过”“真实样本验证通过”“性能实测完成”，不要混用这些状态。

## CUDA 补齐进展（最新）

用户要求继续补齐 CUDA，并询问是否需要空出显卡。当前继续代码与编译；尚未恢复 GPU/CPU 测试。

- CUDA 13.4 / GCC 14 / SM120 的独立构建路径已实现，区别于此前仅调度库的编译。
- 上游 nvcc 内部无限并行已改为单线程编译；不依赖 GPU 探测来选择架构。
- 单卡 UUID 选择、设备节点映射、共享库哈希核验、GPU 进程/整卡显存采样与汇总代码已写。
- 新增显式 GPU 合成测试入口，覆盖 G1/G2 对照与分块边界；仅编译，不执行。
- 建议后续腾出 RTX 5070 Ti 一张卡。只读查询时该卡使用 8912 MiB，5060 Ti 使用 12542 MiB。
- 操作命令及隔离、采样口径见 GPU.md；当前没有 GPU 正确性或性能结论。

CUDA 补齐编译结果：完整 CUDA 后端（device/field/G1/G2 MSM）、Go 主程序和测试二进制编译通过。
`cuobjdump --list-elf` 静态确认 curve 库包含 3 个 SM120 cubin；未执行 cubin 或测试程序。
`readelf` 发现 curve 后端依赖嵌套目录中的 field 后端，运行入口已将所有已核验的库目录加入
受控 LD_LIBRARY_PATH，避免依赖递归 dlopen 的遍历顺序。Python 语法、格式检查通过。
仍待实际驱动加载、单卡隔离、GPU 算术与原 VK 下真实证明验证，不把编译通过当成运行成功。

## 5070 Ti 实测进展

- GPU 空闲确认：5070 Ti 90 MiB，5060 Ti 仍由其他程序使用；仅映射前者。
- CPU 接口/错误收尾及 Python 统计测试通过；初始 GPU 小向量测试通过。
- 首份真实 deposit 失败，保留 `gpu-deposit-smoke-20260924-01`，不能计作成功样本。
- 扩充不同基点与完整位宽标量的向量后，在 65537 点用例复现无效曲线点；17/1024 点通过。
- 显式将 ICICLE `nof_chunks` 设为 1、继续由 Go 外层分块，原失败向量通过。
  定位到此版本的内部多分块路径相关，未宣称已查清内部 CUDA 指令级根因。
- 配置扩展 key 的四处 C.CString 也加入配对 free；错误日志只输出允许的固定码，不记录证明数据。
- `gpu-deposit-smoke-20260924-02` 原参数真实证明及 CPU Verify 成功，GOMAXPROCS=8，外部分块65536，Prove 4.421 秒。
- 新增 `--backend cpu-msm --cpu-control`：同一个 CUDA 链接二进制执行 CPU MSM，不映射 GPU；用于公平对照。
- 下一步同二进制、GOMAXPROCS=32、三类9样本各1轮预热+2轮测量，串行 CPU/GPU 对照。

## 首轮 GPU 对照完成

CPU/GPU 各27份归档证明（9预热+18测量）全部通过原 CPU verifier；同一二进制与源码，GOMAXPROCS=32。
正式样本每场景6次：bridge 7.075→4.733秒（-33.1%），deposit 4.345→2.705秒（-37.7%），withdrawal 4.445→2.799秒（-37.0%）。
GPU 进程显存采样峰值452 MiB，退出后整卡回到90 MiB。CPU/GPU RSS高水位16.55/16.05 GiB，不能据此宣称长期泄漏已排除。
共享主机背景 CPU 负载仍存在（全程粗估平均5.24/4.43核），结果属于初步对照；并非完整 prove-proxy 端到端加速比。
报告：[lab-private/gpu-first-comparison-20260924/REPORT.md](lab-private/gpu-first-comparison-20260924/REPORT.md)。
当前测试已结束，没有留后台证明任务。下一步：更干净窗口复测、长时混合证明、GPU分块/传输调优；参数加载仍为独立待办。

## 内部多分块错误调查

用户要求调查多分块能否进一步加速及大向量为何错误。新增内部块数选择与独立400项对照矩阵。
修复前：21/400错误，全部为 Montgomery 点输入；普通编码点输入200/200通过。包括G1/G2，1/2/4/8/自动分块，1024至262145点，零标量/无穷远、完整位宽标量。
只补 upload_points 的 producer-stream event 依赖后，同样400项全通过。该修复没有改数学算法。
已定位：内部后续块在producer stream异步拷贝，Montgomery转换使用新stream，但缺少拷贝完成→转换开始的依赖。
另发现中间块 return_buckets 提前返回跳过辅助 stream/event 清理。单独补丁编译完成；两补丁共同作用下400项仍全部通过。因果验证保留为两步。
修复前后库、测试二进制、指纹与日志归档在 lab-private/gpu-pipeline-before-20260924 和 gpu-pipeline-after-20260924。
保持内部1块为默认，新增 --msm-internal-chunks 供显式实验。内部4块、外层262144点，三类归档样本共9份全部通过原CPU verifier。
新增交错分块计时：每配置1预热+8测量，144次计算全部与CPU相同。262144点时2块G1/G2减少约4%/5%；1048576点时4块22.515→18.012ms、56.423→44.063ms，减少20.0%/21.9%。8块反而略慢。
这是传输+转换+MSM+结果转换的局部收益，不能换算成整份证明收益。标准GPU边界/生命周期测试及Python统计测试通过。
报告：[lab-private/gpu-pipeline-analysis-20260924/REPORT.md](lab-private/gpu-pipeline-analysis-20260924/REPORT.md)。

1048576点/内部4块也完成三类9份真实证明并全部通过原CPU verifier，两种窗口累计18份通过。默认配置未改；下一轮可将1048576/4作为完整证明性能及长时显存对照候选。当前GPU工作负载全部结束。

## 基于4块继续推进（本轮完成）

用户确认基于4块继续。本轮按A1→B1→B2→A2固定二进制进行完整Go证明配对，A=262144/1，B=1048576/4；每次9预热+9正式证明。72份全部通过原CPU verifier，每配置每场景6个正式样本。

- bridge：4.280→3.875秒，减少9.5%。
- deposit：2.453→2.178秒，减少11.2%。
- withdrawal：2.529→2.325秒，减少8.1%。
- 四轮RSS高水位约16.00GiB、swap=0；进程显存采样峰值A=388–452MiB，B=708–804MiB。共享主机背景CPU粗估2.1–2.4核，不称为绝对干净的生产基线。
- 此处为完整Go groth16_prove阶段，不是完整Rust/plonky2/RPC/FFI时间；窗口与块数同时改变。

Go CLI、Python runner默认值已统一为1048576/4；engine的缺省内部块数也改为4。新默认GPU边界及65537点不同基点测试通过，Python统计测试通过。显式262144/1仍可复现旧配置。
使用不传分块参数的命令验证新默认：9预热+45测量，共54份混合证明全部通过，间隔2秒、无强制GC或trim。
证据和报告：[lab-private/gpu-c4-paired-analysis-20260924/REPORT.md](lab-private/gpu-c4-paired-analysis-20260924/REPORT.md)。
参数加载仍保留为独立待办；本轮未修改生产部署。

连续回放完成：含加载约362秒，53个请求间空闲窗口进程显存均228MiB，进程显存采样峰值708MiB；RSS高水位16.644GiB并出现回落，Go堆随GC下降，swap=0。本轮没有空闲显存随请求增长的迹象，有限回放不能排除所有长期泄漏。
本轮合计126份真实证明通过（4块90份，旧配置36份）。测试均已退出，5070 Ti恢复90MiB空闲占用。新默认1048576/4已完成构建、边界测试及真实回放；参数加载和实际FFI集成继续作为后续事项。

## 推送完成与下一阶段定位

4块代码 `3c9d4ec` 已推送至 QEDProtocol/gnark-plonky2-verifier 的 `lab/groth16-benchmark-20260924`；
psy-memory 实验记录 `fe521da` 已推送至 `feat/rollback-delete`。未合并或部署。

新增 computeH 计时和 MSM 等锁/持锁/同步 ICICLE 调用时间，分析脚本检查每个测量请求的四项阶段与五次 MSM。
`gpu-c4-stage-profile-20260924` 的18份证明全部通过 CPU Verify。期间其他 psy_user_cli 任务占用多核，
本轮不能作为性能基线，因此停止进一步完整证明测速，仅进行轻量代码与正确性验证。
加载后 CPU profile 的 FFT 路径前15项热点占总采样80.23%，主要是有限域乘法和蝶形运算。
这支持下一步关注 computeH 的 GPU NTT，但不能直接推断墙钟加速比。

独立 NTT 测试使用原 gnark 根与显式 canonical 编码，长度16/1024/65536、正逆变换、普通/coset共12项逐元素一致。
初次运行 `gpu-ntt-compat-20260924` 通过；补齐 t.Run 子goroutine锁线程和选卡后，
`gpu-ntt-compat-threaded-20260924` 再次12/12通过。未将 NTT 接入 prover。
新增代码编译通过，Python统计测试2项通过，gnark两个补丁从原始模块重放后的完整源码树与指纹一致。
后续顺序：完整computeH逐元素对照→生产规模域与显存释放→真实证明原CPU验证→空闲时公平性能回放。

## 完整GPU computeH与重复验证

新增可选 `--gpu-h`，把七次NTT与逐点计算放到GPU，保留原gnark参数、solver及CPU verifier。
原CPU实现作为独立参考，验证补零/coset/bit-reversed输出；4M→8M→4M逐元素一致。
检查发现固定ICICLE的domain release遗漏cudaMallocManaged twiddles释放，修复前后单独对照确认；
另将任意coset幂表从串行CPU改为同流GPU生成，再将host编码转换限制为最多8线程。
domain与设备缓冲区逐次释放；失败返回不回退CPU，prover等待H/过滤任务结束，错误H不启动MSM。

固定二进制BAAB（B1→A1→A2→B2，GPU H/CPU H）共72份证明全部通过，每配置/场景6个正式样本。
Go Prove中位数：bridge 3.920→2.219秒（−43.4%），deposit 2.213→1.466秒（−33.8%），withdrawal 2.315→1.622秒（−30.0%）。
后台CPU粗估1.35–2.22核；CPU H对照RSS峰16.194–16.448GiB，GPU H为15.374–15.510GiB。
这只是Go阶段收益，不能直接用于Rust/FFI整体时间。默认保持CPU H＋GPU MSM1048576/4，GPU H显式启用。

配对后仅补强初始化部分失败与kernel启动失败清理；最终版本小H/域生命周期回归通过，
再完成36份间隔2秒混合证明（9预热+27测量），本轮总计108份真实证明全部验证通过。
35空闲窗口进程显存均230MiB，整卡空闲386–454MiB；进程/整卡采样峰分别1286/1764MiB。
RSS高水位16.137GiB，后段回落，Go堆随GC下降，swap=0；有限回放没有空闲显存累计增长迹象。
最终gnark三补丁、ICICLE六补丁重放与指纹相同；Python统计测试通过，所有原始证据私有归档。
性能和内存分析入口：`dev/proverbench/analyze_gpu_h.py`、`analyze_gpu_h_memory.py`。
实际FFI集成、更多场景/长时并发和参数加载优化继续作为后续工作；未部署。
