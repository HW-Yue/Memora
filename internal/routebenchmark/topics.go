package routebenchmark

func topics() []topic {
	return []topic{
		{Slug: "route-fallback", Chain: []term{
			{"runtime-contracts", "runtime contracts", "运行时契约", "Host and engine execution contracts / 宿主与引擎执行契约"},
			{"discovery", "bounded discovery", "有界发现", "Database and Table discovery boundaries / 库表发现边界"},
			{"semantic-routing", "semantic routing", "语义路由", "Explicit AI-maintained Route navigation / 显式 AI 语义路由"},
			{"candidate-miss", "candidate misses", "候选未命中", "Incorrect predictor locations / 预测候选错误"},
			{"snapshot-fallback", "snapshot-safe fallback", "快照安全回退", "Fallback without mixing Route snapshots / 不混用快照的回退"},
			{"rowid-verification", "RowID SQL verification", "RowID SQL 回表", "Final facts require exact SQL Row reads / 最终事实必须 SQL 回表"},
		}, RelatedEN: "How does navigation recover from a wrong predicted location before reading the final fact?",
			RelatedZH: "预测位置选错后，导航怎样恢复并在读取事实前完成校验？",
			OverlapEN: "When candidate prefetch misses, which rule preserves Route snapshot correctness and final Row verification?",
			OverlapZH: "候选预取未命中时，哪条规则同时保证 Route 快照与最终 Row 校验？",
			SynonymEN: "Locate the branch-misprediction recovery and exact-row lookup rule.",
			SynonymZH: "查找分支误预测恢复与精确行回查规则。"},
		{Slug: "wal-reclaim", Chain: []term{
			{"engine-architecture", "engine architecture", "引擎架构", "Storage engine architecture / 存储引擎架构"},
			{"durable-storage", "durable storage", "持久化存储", "Durability and recovery components / 持久性与恢复组件"},
			{"redo-log", "redo log", "重做日志", "Committed redo record stream / 已提交重做日志流"},
			{"checkpoint", "checkpoint publication", "检查点发布", "Durable recovery start publication / 持久恢复起点发布"},
			{"retention-frontier", "retention frontier", "保留边界", "Old log coverage boundary / 旧日志覆盖边界"},
			{"segment-reclaim", "WAL segment reclaim", "WAL 段回收", "Safe deletion of checkpoint-covered WAL segments / 安全删除已覆盖日志段"},
		}, RelatedEN: "After a checkpoint covers old redo, how may disk space be released safely?",
			RelatedZH: "检查点覆盖旧 redo 后，怎样安全释放日志占用的磁盘空间？",
			OverlapEN: "Which boundary allows old durable logs to be deleted after page publication without harming recovery?",
			OverlapZH: "数据页发布后，哪条边界允许删除旧持久日志而不破坏恢复？",
			SynonymEN: "Find the checkpoint-covered redo cleanup rule.",
			SynonymZH: "查找 checkpoint 覆盖后的 redo 清理规则。"},
		{Slug: "package-trust", Chain: []term{
			{"distribution", "distribution", "分发", "Portable product distribution / 可移植产品分发"},
			{"database-package", "Database Package", "数据库包", "Independent semantic Database packages / 独立语义数据库包"},
			{"trust-policy", "package trust policy", "包信任策略", "Explicit trust and install policy / 显式信任与安装策略"},
			{"signature", "package signature", "包签名", "Publisher identity and artifact signature / 发布者身份与制品签名"},
			{"revocation", "signature revocation", "签名撤销", "Rejected publisher/version state / 发布者或版本撤销"},
			{"trusted-upgrade", "trusted package upgrade", "可信包升级", "Upgrade only from verified non-revoked artifacts / 仅从可信未撤销制品升级"},
		}, RelatedEN: "How is an installed knowledge package upgraded without trusting a revoked publisher artifact?",
			RelatedZH: "已安装知识包怎样避免从已撤销的发布者制品升级？",
			OverlapEN: "Which rule combines signature verification, revocation, and package version upgrade?",
			OverlapZH: "哪条规则同时约束签名验证、撤销状态与数据库包版本升级？",
			SynonymEN: "Locate the non-revoked signed bundle update policy.",
			SynonymZH: "查找未撤销签名包的更新策略。"},
		{Slug: "schema-migration", Chain: []term{
			{"semantic-model", "semantic model", "语义模型", "AI-maintained schemas and modules / AI 维护的结构与模块"},
			{"schema-lifecycle", "Schema lifecycle", "Schema 生命周期", "Versioned schema evolution / 版本化结构演化"},
			{"impact-plan", "migration impact plan", "迁移影响计划", "Bounded affected rows and contracts / 有界影响范围"},
			{"row-migration", "Row migration", "Row 数据迁移", "Transform existing semantic Rows / 转换既有语义记录"},
			{"verification", "migration verification", "迁移验证", "Post-migration invariant verification / 迁移后不变量验证"},
			{"compensation", "Schema compensation", "Schema 补偿", "Compensating recovery after failed evolution / 演化失败后的补偿恢复"},
		}, RelatedEN: "What happens when a verified Schema evolution still needs to be undone safely?",
			RelatedZH: "Schema 演化完成验证后仍需安全撤销时应怎样处理？",
			OverlapEN: "Which mechanism recovers from a migration failure without erasing prior Row history?",
			OverlapZH: "迁移失败后，哪种机制能恢复且不抹除既有 Row 历史？",
			SynonymEN: "Find the compensating schema rollback record.",
			SynonymZH: "查找追加式 Schema 回滚补偿记录。"},
		{Slug: "source-review", Chain: []term{
			{"knowledge-ingestion", "knowledge ingestion", "知识摄取", "Temporary source reading and semantic absorption / 临时读取与语义吸收"},
			{"source-inventory", "source inventory", "资料清单", "Bounded source units and fingerprints / 有界资料单元与指纹"},
			{"coverage", "coverage tracking", "覆盖跟踪", "Read and unread source ranges / 已读与未读范围"},
			{"isolated-review", "isolated review", "隔离复核", "Challenge-bound independent review / challenge 绑定的独立复核"},
			{"conflict-gate", "source conflict gate", "来源冲突门", "Unresolved critical fact conflicts / 未决关键事实冲突"},
			{"source-receipt", "committed Source Receipt", "已提交 Source Receipt", "Proof that reviewed semantic absorption committed / 复核吸收提交证明"},
		}, RelatedEN: "Which receipt proves that independently reviewed source material was actually committed?",
			RelatedZH: "哪种收据能证明经过独立复核的资料确实已经提交？",
			OverlapEN: "After coverage and conflict checks, what distinguishes review completion from a committed absorption?",
			OverlapZH: "覆盖和冲突检查后，什么能区分复核完成与资料真正提交？",
			SynonymEN: "Locate the challenge-reviewed ingestion commit proof.",
			SynonymZH: "查找 challenge 复核后的摄取提交证明。"},
		{Slug: "multi-device-sync", Chain: []term{
			{"resilience", "resilience", "韧性", "Backup, recovery, and future synchronization / 备份恢复与未来同步"},
			{"change-log", "committed change log", "提交变化日志", "Ordered logical transaction changes / 有序逻辑事务变化"},
			{"portable-backup", "portable backup", "可搬迁备份", "Integrity-bound Instance backup / 完整性绑定的实例备份"},
			{"transport", "authorized transport", "授权传输", "Authenticated encrypted change transport / 认证加密变化传输"},
			{"multi-device", "multi-device synchronization", "多设备同步", "Bidirectional device change exchange / 设备间双向变化交换"},
			{"sync-conflict", "synchronization conflict resolution", "同步冲突解决", "Explicit resolution for concurrent semantic revisions / 并发语义 revision 显式解决"},
		}, RelatedEN: "How are concurrent semantic revisions resolved when two devices exchange changes?",
			RelatedZH: "两台设备交换变化时，并发语义 revision 应怎样解决？",
			OverlapEN: "Which rule handles bidirectional device updates that share history but diverge concurrently?",
			OverlapZH: "共享历史的双向设备更新发生并发分叉时，由哪条规则处理？",
			SynonymEN: "Find the cross-device merge-conflict protocol.",
			SynonymZH: "查找跨设备 merge conflict 协议。"},
	}
}

func decoyTerms() []term {
	values := [][2]string{
		{"buffer-pool", "缓冲池"}, {"btree-search", "B+树查找"}, {"snapshot-visibility", "快照可见性"},
		{"write-lock", "写锁"}, {"admin-timeline", "管理时间线"}, {"route-trace", "路由跟踪"},
		{"catalog-metadata", "目录元数据"}, {"row-history", "行历史"}, {"relationship-graph", "关系图"},
		{"query-parser", "查询解析器"}, {"mutation-policy", "变更策略"}, {"backup-restore", "备份恢复"},
		{"point-in-time-recovery", "时间点恢复"}, {"replication", "复制"}, {"page-allocation", "页面分配"},
		{"dirty-flush", "脏页刷盘"}, {"index-generation", "索引代际"}, {"catalog-alias", "目录别名"},
		{"row-document", "动态文档"}, {"revision-diff", "版本差异"}, {"semantic-health", "语义健康"},
		{"route-reshape", "路由重塑"}, {"feedback-receipt", "反馈收据"}, {"conversation-checkpoint", "对话检查点"},
		{"package-fork", "数据库包分支"}, {"package-merge", "数据库包合并"}, {"release-artifact", "发行制品"},
		{"launch-service", "用户服务"}, {"mcp-adapter", "MCP适配器"}, {"go-sdk", "Go SDK"},
		{"context-budget", "上下文预算"}, {"lexical-location", "字面位置"}, {"candidate-generation", "候选代际"},
		{"cpu-exact-scan", "CPU精确扫描"}, {"speculative-prefetch", "投机预取"}, {"permission-scope", "权限范围"},
		{"source-anchor", "来源锚点"}, {"logical-undo", "逻辑撤销"}, {"schema-alias", "Schema别名"},
		{"database-discovery", "数据库发现"},
	}
	result := make([]term, 0, len(values))
	for _, value := range values {
		english := value[0]
		result = append(result, term{Slug: value[0], English: english, Chinese: value[1],
			Purpose: english + " knowledge boundary / " + value[1] + "知识边界"})
	}
	return result
}
