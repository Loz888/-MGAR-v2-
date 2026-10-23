# 独立 incident Oracle

orders.json 的固定业务要求：paid 的 order_id 去重后 101=100、102=50，总和 150；
pending 103 不计入。输入中 101 的重复事件必须保留在输入指纹里。
fixed_aggregate.go.txt 是人工固定的正确参考实现，不是 Agent 或 Verifier 生成的答案。
现有 go-repo/aggregate.go 故意保留错误，实际 Go Test 必须退出 1；正确参考副本退出 0。

重执行配置 oracle.v1 固定 Go 1.27.3 linux/amd64、package example.invalid/mgar/incident、
TestPaidDeduplicatedTotal。完整源码树指纹随实际测试记录，不使用目录名充当源码版本。
SQL oracle 使用本批自己建立的合成 orders 表、只读登录与活跃 exported snapshot，
不读取真实订单业务库。规范化同时保留行多重性和精确数字语义。
