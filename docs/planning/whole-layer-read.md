# 这份文档搬走了

`whole-layer-read.md` 已按「问题 / 实现 / 规划」三分移到
[`docs/implementation/design/whole-layer-read.md`](../implementation/design/whole-layer-read.md)。

这里留一个指针，因为 `skills/memora/` 的 `jev-tree.md` 与 `jev_tree.py` 直接引用了这个路径，
而 Skill 在仓库外还有三处安装位，本次沙箱不允许同步它们——**改 Skill 里的引用要按 AGENTS.md
的收尾动作跑 `scripts/sync-skill.sh`，那是单独一件事**。
