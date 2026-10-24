# 冻结任务集 mgar-dataset.v1

当前 manifest SHA-256：`0b48a86008574d0a8bdbde1d88d37e718d45e75fe33363075fdcd48f0eb2d511`。

| 集合 | 数量 | 用途 |
| --- | --- | --- |
| offline | 60 | simple / parallel / fanin / task_failure / partial_replan / stale_context / reordered_duplicate / budget_exhausted / refusal / policy_denied，每类 6 条。 |
| live | 24 | holdout，简单/中等/困难各 8 条；incident/analysis 各 12；6 条拒绝负例。没有运行模型。 |
| development | 8 | 独立 seed 与 case ID；调 Prompt 只使用此集，不针对 live holdout 调参。 |

所有金额是合成整数单位。语义真值计算去重 paid 总额、paid 订单数、重复 paid 事件数；pending 不计入。incident 需要测试环境修复及独立沙箱执行，analysis 不要求代码修改。输入历史用当前金额减一构造可识别的旧版本，旧/新期望不能混用。拒绝范围包括证据缺失和未经授权的生产删除要求；这些只是合成负例，不会执行生产动作。

检查：`python eval/oracle/validate_manifest.py`；验收：`python -m pytest -q tests/e2e/fixtures_test.py`。测试实际为全部 60 条工程用例创建独立 PG schema、独立 reader，并验证只读拒绝、跨 schema 拒绝及同种子复现。Oracle 还有人工计算的 150/2/1 回归与伪造数值/自报 verified 的反例。

数据/Oracle 修改流程：审查源码和数据变更，选择递增版本，再运行 `python eval/oracle/freeze.py --version mgar-dataset.vN`，重新验收并跑全部实验组。旧版本锁不覆盖；历史 manifest 和 Git 版本保留。freeze 没有模型结果输入参数，validate 不修复文件。不得删失败样本或把内部 Verifier 作为唯一外部 Oracle。

P7-01 仅交付冻结数据与独立语义判断；真实控制面闭环、组间公平对照、统计和混沌负载分别在后续卡验证。语义 Oracle 的 `execution_verified=false` 是明确边界，需要外部可信重执行证据才能证明运行。
