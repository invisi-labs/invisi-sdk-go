# Go retained preparation：9→7 次实际HTTP

## 结果

新增 `WalletSession.InitializeShieldPreparation(ctx, token)`。已验证的原生同步也能为其真正捕获过的Token复用准备锚点。普通签名出资、完整长地址的 managed Shield 在可信锚点命中时，每笔实际HTTP从9降到7。未启用预热的实时准备仍为9，已有接口不需要强制改成历史准备。

7次由prepare中的当前网络＋最新payer nonce2次、quote2次、submit3次组成；发现缓存查找和授权签名0次。历史header/Token材料用于构造，不声称是latest。nonce从不跨交易缓存。

显式预热额外6次一次性请求，包含实际协议服务部署查询、头、网络、按块Core代码/VERSION与Token代码。节点不支持哈希选择器时额外探测和旧式规范头检查也计数。初始化成本见下表，未计为免费。成功原生full refresh复用自己已经捕获过的材料；InventoryOnly或缺Token材料不激活该能力，单独恢复JSON快照也不能激活。

## 接入

```go
if err := walletSession.InitializeShieldPreparation(ctx, token); err != nil {
    return err
}
op, err := walletSession.PrepareShield(ctx, sdk.ShieldPreparation{
    TokenAddress: token,
    Outputs: outputs,
    Funding: sdk.FundingSource{Payer: payer, Amount: amount}, // SigNonce=nil
})
```

要求采用SDK拥有的 `chain.HTTPBinding`。如果成功的原生full refresh已经捕获该Token，则自动拥有可复用锚点，通常无需再显式预热。构造锚点绑定实际连接/epoch、协议及服务客户端、钱包scope、代际/reset revision、检查点、Token和初始化所有权。材料缺失走保守路径；发现当前已持有锚点失效时可拒绝这次准备，重新初始化/同步后继续。

## 同一套实测

与上一轮同一支付业务、统计边界与HTTP fixture。原生before为本轮开始的9请求源码，after为审查后的新源码；相同最终诊断driver、相同原生工厂，不采用早期25请求provider。每组3fixtures×2笔；同reader/client/session/store复用、第一笔journal字节保持；公开payer01/02与真实读出的nonce7/14，业务30+fee5，上限10；无私有输入/proof/registry/permit/schedule。

链、协议服务和广播器实际走loopback HTTP，每个响应人为加20ms，四组在我方重编译/测试结束后串行运行。交易时间为nonce/prepare→lookup→quote→授权签名→submit各阶段之和，包含本地CPU；编译、进程启动不计入。每笔正常广播被最底层transport拒绝，无真实链上提交。机器仍有其他应用背景负载，3组耗时仅为趋势。

| 场景 | 每笔请求 | 首笔中位ms（范围） | 第二笔中位ms（范围） |
|---|---:|---:|---:|
| 修改前：实时准备 | 9 | 207.49 (206.94–217.35) | 214.09 (209.86–216.16) |
| 修改后：未启用预热 | 9 | 220.11 (207.55–220.66) | 212.74 (212.26–213.73) |
| 修改后：显式预热 | 7 | 189.94 (185.54–193.27) | 189.73 (185.15–193.12) |
| 修改后：预热／旧选择器节点 | 7 | 193.41 (190.32–194.40) | 191.50 (187.74–192.51) |

### 初始化和首次总成本

| 场景 | 冷启动请求/中位ms | 初始化请求/中位ms | 含冷启动和初始化的首笔总请求/中位ms |
|---|---:|---:|---:|
| 修改前：实时准备 | 7 / 158.13 | 3 / 61.68 | 19 / 428.78 |
| 修改后：未启用预热 | 7 / 158.65 | 3 / 63.77 | 19 / 439.94 |
| 修改后：显式预热 | 7 / 155.12 | 9 / 197.51 | 23 / 541.13 |
| 修改后：预热／旧选择器节点 | 7 / 157.24 | 11 / 238.80 | 25 / 588.96 |

实时模式冷启动7＋广播器初始化3＝10次；显式预热后为7＋9＝16次。首笔总请求19→23，两笔总请求28→30；按固定每笔9/7的本场景计数，累计3笔均为37次，第4笔起预热模式总请求更少。耗时摊销取决于并行、网络与密码学，不由请求数直接等比例推算。预热适合支付按钮前完成并由同一session多笔复用；它减少时间敏感交易路径，不能据此声称冷启动更快。

旧选择器节点的预热成本更高，但成功初始化后的每笔仍为7；能力拒绝探测失败被算进初始化请求。只对明确不支持选择器格式的响应作安全回退；模糊参数错误、未知/非规范区块、RPC失败仍拒绝。

## 安全与恢复

- 新锚点仅保留历史构造事实；默认nonce仍每笔执行`eth_chainId`＋`nonces(payer)` at latest，显式nonce只是调用者选择并保留网络检查。deadline按现有历史构造规则取保留时间与主机当前时间的较大者，资金授权过期也拒绝。
- Native provider始终保持可见，不用通用SnapshotProvider隐藏连接身份。范围仅普通长地址签名Shield；自定义provider/verifier、detached/manual、私有输入、注册表依赖、Credit、permit与其他形态保留相应检查。
- 新初始化或刷新先退休旧ticket，失败或晚返回不得恢复旧权限。活动scan、分阶段refresh/decrypt贯穿使用自己的开始ticket。
- 准备完成、钱包写入回调内、私有提交后和首次网络发送前均核对相应连接/代际/检查点。原检查点读取位于最终规范头fence之前，防止拼接不同分支。
- 新在线保留模式与旧离线准备模式分开。仅内存中的真实receipt允许本地报价后授权及执行委托；JSON恢复不再拥有它，首次发送前走完整校验，手工签名/导出不能误用离线豁免。
- 新模式journal为v2，mode/version一致性必须通过；旧SDK在签名/导出前拒绝v2。实际当前writer→冻结旧reader测试通过，v1对照仍可读。需要兼容SDK恢复新记录；ABI和广播请求协议没有因此改变。
- 完整报文与不确定状态仍在transport前原子持久化。缓存退休不阻断已冻结请求的原客户端原样重放；已知ID只查状态。不重新选nonce、重签或改包。先前已导出授权不会因后来结构化拒绝而解除。

## 验证与取证

审查发现并修复了：最后持久化期间退休缓存仍首发、同步绕过当前服务部署认证、原检查点被新值覆盖、旧scan抢占后启动初始化、旧SDK静默误读新模式，以及最后规范头fence之后又读缺失旧检查点的混合分支窗口。跨session共享连接的错误网络退休也有实际HTTP回归。每个关键问题均先复现再修复；最终独立审查无未解决发现。

最终focused race回归覆盖初始化/同步来源、nonce7/14、超时与取消、epoch/代际和存储回调竞态、失败/重叠初始化、JSON各阶段、手工路径、网络两道fence、拒绝回滚与未知ID原样恢复/已知ID状态查询。

- `module-checks.log`：9模块race tests、vet、build全部通过。
- `archive-checks.log`：8独立模块打包、解包后的tests、vet、build全部通过。
- `downgrade-check.log`：真实新writer与冻结旧reader跨版本验证通过，临时公开fixture内容已删除。
- `review-report.md`：五项发现全部关闭，附审查的实际源码hash与限制。
- 四组0-delay功能检查与最终20ms三组×两笔均通过，24次正常广播在HTTP之前拒绝；legacy组仅3个预期的初始化能力探测失败，其他读取成功。

`before-location.txt`和`before-manifest.json`在修改前冻结9次版本。`prepare.py`独立编译前后源码并记录library/driver/binary SHA；`summarize.py`检查原始HTTP计数、来源/二进制一致性、nonce区块参数、初始化是否采用、新模式v2断言与零写入。所有原始记录、运行日志及hash在`results/`，范围和每个样本在`summary.json`。默认实时模式对照验证未启用新能力仍为9。

本轮未rebase develop、未merge/commit/publish、未改TS/Rust、无实网广播。这些是本地实际HTTP与原生SDK测试，不能当作App/设备或公共节点实际耗时。
