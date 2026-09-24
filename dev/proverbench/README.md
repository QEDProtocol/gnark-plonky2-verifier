# 独立 Groth16 调优实验室

当前状态：**GPU实验默认1048576点窗口/内部4块；本轮ABBA及连续回放126份真实证明全部通过原CPU verifier。**
首轮CPU/GPU各27份对照已完成，详细阶段及局限见GPU.md。
旧版 CPU 框架 5 项测试通过，累计 63 次真实样本证明验证成功；正式计时受其他负载干扰，不能作为干净基线。
MSM 接口和 GPU 实验适配已运行验证，见 [GPU.md](GPU.md)。不存在自动测试、定时任务或服务。

基于线上 wrapper commit `aebd3683cd6a2bcf83113113c142344e94d05367` 的独立本地 clone，分支
`lab/groth16-benchmark-20260924`。原有工作目录和生产部署没有改动。
Go 1.22.3，原 `go.mod/go.sum`，独立编译缓存、二进制与结果目录。
普通构建不会编译实验代码：新增 Go 文件受 `proverbench` build tag 保护。

## 范围与测量

- 默认使用已归档线上 FFI 样本：bridge / deposit / withdrawal，各 3 份。
- 只读加载现有 R1CS、PK、VK，不重新生成生产参数，不调用 HTTP 或生产服务。
- 仅加载所选场景的参数。`--scenario all` 同时保留三类参数，单场景的常驻内存不能直接与三场景结果比较。
- 计时：加载 circuit / PK / VK、读输入、解析/构造 assignment、NewWitness、Groth16.Prove、CPU Verify、原生 proof JSON 序列化。
- gnark 内部日志额外给出 `solver` 和 `prover_compute`；MSM 实验构建另外记录五次 MSM 调用时间。它们属于 `groth16_prove` 内部时间，不能重复相加；MSM 调用区间也可能重叠。
- 每份证明必须用现有 CPU verifier 与 VK 验证成功；失败立即终止，不重试掩盖问题。
- JSONL 记录每请求序号、样本/场景、预热标记、阶段耗时；记录 Go heap/GC、RSS/HWM/swap，外部 `/proc` 观测 CPU 时间、PSS、匿名内存和线程数。
- 每阶段只测时间；内存默认每秒及请求边界采样。`--sample-interval 0` 关闭周期内外观测，仍保留请求边界快照。
- `--warmup` 按完整样本轮次计数。预热进入原始日志，但排除汇总耗时；RSS 高水位仍包含加载和预热。
- 可选 CPU/heap pprof，不强制 GC。只有显式 `--gc-between` 才主动 GC，并写入实验配置。

这是独立 Go 阶段基准，不包含 Rust、C ABI、HTTP、完整响应生成与网络延迟。输入准备由生产
`GenerateProof` 的对应代码原样提取（删除数值打印）；生产函数本身未改。序列化测量目前只覆盖
原生 proof JSON，不含生产额外的 Solidity 字段与 VK JSON。CPU 基线禁用 cgo，不能当作生产
Rust/CArchive 二进制的逐字节复现。真实样本运行验证已通过；无干扰性能基线仍待完成。

## 编译（不会运行测试）

在仓库根目录执行：

```sh
python3 dev/proverbench/lab.py build
# 同时编译测试二进制，依然不执行测试：
python3 dev/proverbench/lab.py build --compile-tests
```

默认使用相邻归档中的 `toolchain1223/go/bin/go`，可用 `--go /path/to/go` 指定另一个 Go 1.22.3
路径。复用只读本地 Go module cache，`GOPROXY=off`、`-mod=readonly`，不自动下载依赖。
编译 `-p 1`、`GOMAXPROCS=1`、nice 19，降低对同机后台测试的影响。
生成 `lab-bin/proverbench` 和 `build.json`，记录源码、二进制、工具链哈希与构建命令。
源码修改后必须重新 build，run 会检查哈希。

## 以后手动运行

以下命令**会执行证明负载**，请在其他 agent 不进行重型编译或回放时运行：

```sh
# 三种场景各 3 个样本，1 轮预热 + 3 轮测量：共 36 份证明。
python3 dev/proverbench/lab.py run --name cpu-all-baseline \
  --scenario all --warmup 1 --cycles 3 --gomaxprocs 8

# 单样本反复证明，请求间隔 5 秒，观察间歇负载内存。
python3 dev/proverbench/lab.py run --name deposit-paced \
  --sample deposit-00 --warmup 1 --cycles 12 --interval 5 --gomaxprocs 8

# 独立 profile 实验，避免把采样开销混入普通基准。
python3 dev/proverbench/lab.py run --name bridge-profile \
  --scenario bridge --warmup 1 --cycles 2 --profile

python3 dev/proverbench/lab.py report lab-private/cpu-all-baseline
python3 dev/proverbench/lab.py compare lab-private/run-a lab-private/run-b
```

可调：`--gomaxprocs`、`--gogc`（默认 100）、`--gomemlimit`（默认 off）、`--gc-between`、
`--sample-interval`、`--warmup`、`--cycles`、`--interval`、`--scenario`、`--sample`。
当前请求串行，避免 CPU/GPU 尚未验证的共享 key 并发造成混淆。以后可另建并发模式。

每次运行先核验所用文件的归档 SHA256，结果目录必须是新名字，不覆盖旧证据。bubblewrap
关闭网络，挂载只读文件系统和输入，仅结果目录及私有 `/tmp` 可写。若主机策略禁止 bubblewrap，
命令会失败，不会自动降级到无隔离运行。当前 CPU 隔离环境不暴露 GPU 设备。

结果 `lab-private/<name>/` 包含：

| 文件 | 内容 |
|---|---|
| run.json | 参数、样本哈希、构建信息、CPU 型号/affinity |
| proverbench | 此次运行的二进制副本，用于后续 pprof |
| metrics.jsonl | 内部阶段与 Go/进程内存指标，不写证明内容 |
| external.jsonl | 外部 `/proc` 指标；周期观测关闭时不生成 |
| exit.json / summary.json | 退出状态、完整性、分场景中位数/P95/范围 |
| cpu.pprof / heap.pprof | 仅 `--profile` 时生成，保存在私有目录 |

未知 stderr 行被替换成错误标记，不保存原文。第三方库 stdout 丢弃。panic 仅记录固定错误码，
不输出 witness、证明、key 或原始 panic。小样本 P95 只是描述，不作为稳定性能结论。
比较工具保留配置差异警告；不同 CPU/affinity、样本、预热、GC 或 profiling 不能直接归因于后端。

## 框架测试（CPU接口、统计、GPU向量与真实证明均已验证）

以下命令会执行测试；GPU测试由 GPU.md 中的独立入口显式启动。

```sh
python3 -m unittest discover -s dev/proverbench -v
# 使用与 build 相同的 Go 1.22.3、独立 GOCACHE 和离线 module cache：
GOTOOLCHAIN=local GOCACHE="$PWD/lab-cache" GOPROXY=off \
  ../tmp/prove-proxy-replay-20260921/toolchain1223/go/bin/go \
  test -p 1 -mod=readonly -tags=proverbench ./cmd/proverbench
```

测试覆盖样本筛选/路径限制、未实现 GPU 禁止静默回退、预热和失败请求统计、CPU 小电路正确性
及错误 public input 拒绝。小电路测试的 Setup 仅在内存中生成测试参数，不触碰归档 keystore。
完整验收还需要三场景真实数据全部验证、重复回放和进程结束状态检查。

## 后续 GPU 接入点

新增 `cpu-msm` 与 `icicle-msm` 实验构建，保留原 R1CS/PK/VK 和 CPU Verify，只替换五次 MSM。
GPU 适配代码先完成编译与链接检查，CUDA SM120 后端构建、GPU 单卡隔离运行与显存采样代码已补齐，本轮已运行验证。
`lab.py run --backend icicle-msm` 要求完整 CUDA 构建和明确的 GPU UUID；不会自动回退 CPU。
GPU 可调 `--msm-chunk-size`（外层窗口，默认1048576）与 `--msm-internal-chunks 1|2|4|8`（内部流水线，默认4）。
已修复固定ICICLE版本内部多块的转换stream依赖及中间块句柄清理；400项回归、144次交错计时校验和内部4块两个窗口共18份真实证明通过。
大窗口分块计时另见工作文档，不把MSM局部收益当作整份证明的收益。
详见 [GPU.md](GPU.md) 的范围、构建命令和待验证清单。初步 Go Prove 耗时降低33%–38%，进程显存采样峰值452 MiB；共享CPU负载与短回放限制见工作文档。

## 本轮结果分析

保留对应私有回放目录后，可重算2026-09-24四组ABBA及连续混合回放的统计：

```sh
python3 dev/proverbench/analyze_gpu_c4.py
python3 dev/proverbench/analyze_gpu_c4_memory.py
```

脚本只分析现有指标，不启动证明负载；校验完整性、参数和构建一致性，输出到
`lab-private/gpu-c4-paired-analysis-20260924/`。这是本轮固定实验名的分析入口。
原始证明输入、密钥、日志、依赖镜像和编译产物均不提交。跨仓库过程归档见
`psy-memory/prove_proxy/gpu-report-20260924.md`及同目录汇总JSON；本地连续工作记录见WORKLOG.md。
