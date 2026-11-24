# Go prepare：11次请求的来源与本轮9次路径

## 结论

原11次来自独立nonce预读2次、prepare4次、quote2次、submit3次。独立nonce预读并非Go调用该接口的必需步骤：已有API在`Funding.SigNonce == nil`时，在prepare刚读取的区块高度上选择payer nonce。原版本仅调整调用方式已能11→10；本轮SDK进一步将Token代码与最终规范区块校验合并为一次查询，10→9。两项收益单独设置对照组，没有把调用方式收益全算作SDK代码收益。

本轮保持实时prepare，不将旧的初始化区块伪装成latest。未rebase/merge/commit、未改TS/Rust、未真实广播。

## 原来多出的四次分别是什么

| 原prepare请求 | 参数（公开fixture示例） | 用途 | 本轮处理 |
|---|---|---|---|
| `eth_chainId` | `[]` | 核对部署网络 | 保留prepare这次，使它同时覆盖内置nonce读取；删除外部预读阶段的重复网络检查 |
| `eth_getBlockByNumber` | `["latest", false]` | 当前区块、链时间、deadline | 继续逐笔读取，不作为不变事实缓存 |
| `eth_getCode` | `["0x2222222222222222222222222222222222222222", "0xa"]` | 目标Token存在代码 | 移到准备结束，与下项合并 |
| `eth_getBlockByNumber` | `["0xa", false]` | 持久化前重新核对准备区块 | 由下面的规范链代码查询替代 |

新合并调用形态：

```json
["0x2222222222222222222222222222222222222222", {"blockHash":"<刚读取的准备头哈希>","requireCanonical":true}]
```

它调用`eth_getCode`，要求节点在该确切哈希仍属于规范链时返回代码。[EIP-1898规范](https://eips.ethereum.org/EIPS/eip-1898#specification)定义该选择器语义。代码为空、区块未知、非规范链、RPC/解析失败都不会持久化准备结果。该保证依赖节点正确实现RPC语义，不是SDK独立验证共识；任何读链与后续存储/发送之间仍有时间间隔。nonce本身仍按区块**高度**读取，没有将它描述为hash-pinned。

## 为什么这里是9而非7

当前Go实时prepare保留4次：网络、latest头、nonce、按哈希的Token/规范链组合读取。另有quote2次、submit3次。相较TS/Rust前次7次路径，多出来的两项是实时准备头与Token/规范链读取；前次TS/Rust诊断采用同步/初始化时保留的准备锚点。支付业务和恢复保证相同，准备数据的新鲜度模式有差异。

网络/合约部署与广播器支持等已有初始化缓存继续使用。latest头、链时间及某个区块当前是否规范会变化；Token地址不变也不能把所有代码/状态都视为不变。本轮没有静默把Go的实时prepare改为历史准备。现有历史PreparationSnapshot、自定义适配器等流程保持其原有规则。

## 同一套实际HTTP诊断

与上轮同一支付场景和边界：一个完整长地址Note输出、公开payer01/02、nonce7/14、业务30、fee5、上限10，无私有输入/proof/permit/schedule；每版本3组，每组同reader/client/session/store连续两笔，第一笔journal完整保留。链、协议服务、广播器都走loopback实际HTTP，每次响应加20ms；正常广播在最底层transport之前拒绝。所有组在我方重编译/测试结束后串行运行。

这是受控本地HTTP，不是实网节点或App/设备耗时。冷启动/初始化单列；每笔耗时为nonce前置到prepare、lookup、quote、签名和submit各阶段之和，包含本地CPU工作，排除编译/进程启动。机器其他应用仍有背景负载，3组耗时只作为趋势；所有组请求数稳定。

| 对照组 | 首笔请求 | 第二笔请求 | 首笔中位ms（范围） | 第二笔中位ms（范围） |
|---|---:|---:|---:|---:|
| 原SDK＋独立nonce预读 | 11 | 11 | 253.86 (248.57–261.55) | 259.40 (249.66–261.80) |
| 原SDK＋prepare自动nonce | 10 | 10 | 241.77 (231.13–241.80) | 243.36 (229.86–244.75) |
| 新SDK＋prepare自动nonce | 9 | 9 | 218.28 (206.56–219.69) | 216.19 (212.90–216.37) |
| 新SDK＋不支持选择器节点 | 11 | 10 | 264.06 (257.95–264.45) | 239.05 (232.19–242.22) |

### 冷启动与初始化没有增加

| 对照组 | 冷启动请求/中位ms | 初始化请求/中位ms | 含冷启动和初始化的首笔总请求/中位ms |
|---|---:|---:|---:|
| 原SDK＋独立nonce预读 | 7 / 157.34 | 3 / 65.05 | 21 / 477.25 |
| 原SDK＋prepare自动nonce | 7 / 158.55 | 3 / 66.41 | 20 / 466.73 |
| 新SDK＋prepare自动nonce | 7 / 162.04 | 3 / 65.91 | 19 / 444.98 |
| 新SDK＋不支持选择器节点 | 7 / 158.48 | 3 / 66.52 | 21 / 489.06 |

### 新9次逐项

1. `eth_chainId`：prepare网络检查。
2. `eth_getBlockByNumber("latest", false)`：fresh准备头。
3. `eth_call(nonces(payer), "0xa")`：在该准备高度读取本笔nonce。
4. `eth_getCode(token, {blockHash, requireCanonical:true})`：准备结束前Token代码与规范链核验。
5. `broadcaster_estimateQuote`：付费形态初估。
6. `broadcaster_getQuote`：准确重建后获取签名报价。
7. `eth_chainId`：进入首次submit的网络边界。
8. `broadcaster_getStatus`：当前服务要求。
9. `eth_chainId`：临近发送前网络边界。

prepare中的网络与latest头并行读取；以上按功能列出，实际顺序与区块参数记录在原始trace。首次submit保持进入网络检查、服务状态、发送前网络检查的顺序。授权/签名0网络请求，最终广播写入0 HTTP（被诊断guard拒绝）。所有完整请求的区块参数在`results/*.json`的`attempts[].blockSelector`。

## 不支持新格式的节点

只有明确的方法缺失，或两种精确的selector格式拒绝消息，才允许退回按高度CodeAt＋原Canonical头检查。模糊`-32602`、未知hash、非规范链、裁剪状态、畸形hash、transport错误均终止，不能用另一种读取掩盖状态错误。

“不支持选择器”是唯一新增缓存，绑定HTTPBinding和epoch；没有缓存Token代码、nonce、余额、allowance或签名策略。首次探测失败也计入网络请求，因此该节点首笔11次；第二笔不重复探测，10次。连接epoch变化后重新探测。支持的节点每笔均有真实代码查询。

## 安全范围、回归与校验

仅拥有固定原生HTTP连接、实时managed、签名出资、完整长地址的普通Shield采用组合检查。自定义provider/verifier、detached/manual准备、历史快照、私有输入、注册表依赖、permit、Credit等保持旧校验。钱包checkpoint检查、代际/worker、保留项与OnComplete/store事务仍运行；组合检查在持久化之前。

已通过27个实际wire级场景及补充race测试，覆盖实际选择器、空代码、reorg/未知块、错误码歧义、请求失败、取消、连接失效、过期请求不误退役新epoch、不支持能力缓存及失效，以及custom/manual/快照等保守回退。原子冻结、首次写失败无外发、丢响应原样重放、known-ID status-only、手工授权后的拒绝恢复、已确认交易核验等现有回归随全模块重新验证。

- 最终9模块：race tests、vet、build均通过（`module-checks.log`）。
- 最终8个独立module archives：tests、vet、build均通过（`archive-checks.log`）。
- 独立源码审查发现的模糊-32602回退问题已RED→GREEN；最终无未解决发现（`review-report.md`）。
- 4组×3fixtures×2笔共24笔正常诊断，全部在外发HTTP之前拒绝广播；只有legacy组首次预期的3个能力协商失败，其余HTTP读取成功。
- 11→10 API对照、10→9 SDK对照分别经过0-delay功能验证和20ms正式测量；nonce7/14确实使用，第一笔journal逐字节不变。

源码在修改前冻结。两版均用相同native HTTP工厂/同一最终driver，不使用前轮25次旧provider工厂。`prepare.py`记录源和二进制SHA，`summarize.py`核对binary/source/trace身份、真实请求数、区块参数、拒绝写入及预期fallback探测。before来源见`before-location.txt`。`results/`保留原始四组数据、执行日志及源/二进制manifest，`summary.json`保存完整样本与统计。
