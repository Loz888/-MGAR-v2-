# P7-02 合成业务演示

先按 `docs/operations.md` 启动合成 PostgreSQL 并构建 `.local/sandbox-image.id`。

```bash
bash scripts/demo.sh --provider mock --case offline-001
bash scripts/demo.sh --provider mock --case offline-002
python -m pytest -q tests/e2e/incident_test.py tests/e2e/analysis_test.py
```

第一条是订单异常调查和临时工作树修复；第二条是无需代码的只读业务分析。
入口先检查 P7-01 冻结 manifest，每次创建自己的控制面 schema、只读业务账号、
不可变 CAS 和临时工作树。Planner 的调用也记账；候选 DAG 经验证提交 v2。
Research/Data 在两个独立 Python 进程并行，真实工具经 Go Gateway 授权、预留和执行。
程序化 Verifier 也有独立任务和工具/资源准入，SQL/测试重跑计入同一 Workflow 预算。
Data 使用固定注册查询与独立只读账号；Action 从真实已选输入判断补丁，并在
非 root、禁网、无凭据 Docker 沙箱运行 Go 测试。Verifier 重新运行 SQL/测试。
Aggregator 只消费通过验证的直接输入；每个事实包含原始哈希、任务与计划版本。

输出为一个完整 JSON：持久化最终状态、Python 进程与实际开始/结束时间、
模型/工具预算对账、原始 Artifact、独立验证证据、最终引用、审计及实际接收的 OTLP。
Trace 是已有审计事件的完整投影 `complete_projection`，标为 reconstructed，
不把零时长审计点解释为性能跨度。Mock 用量是 `local_stub` 的字节估算，非真实模型账单。
运行完成只清理本次创建的资源；先导出报告，根 fixture 原始哈希必须不变。

负例接口：`--fault forge-planner|forge-research|forge-data|forge-action|forge-aggregate|wrong-patch`。
`offline-049` 缺证据，`offline-055` 请求生产删除；都进入 blocked。
负例可成功完成实验（进程退出 0），但其 Workflow 必须为 failed/blocked；
执行未确认的 interrupted 或正常用例失败会退出非零。完整 JSON 保留失败详情。

边界：仅固定合成工具和有限人工审阅补丁，不接受任意 SQL、路径、Shell 或生成代码；
Python SDK 本身是受信代码，模型文本不转为宿主代码。模型白名单仅 mock，
不读取独立 oracle 答案；E2E 在运行后单独对照冻结答案。独立源码 fixture 的业务
单元测试在沙箱重跑，动态数据的数值答案另外由独立数学 Oracle 评分。
不声明真实模型能力、跨主机容错或生产就绪。本卡不执行 P7-03 基线或 P7-06 付费评测。
