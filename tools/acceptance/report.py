#!/usr/bin/env python3
"""Build the acceptance report page from the defect register and the run results.

  python3 tools/acceptance/report.py [--out PATH]

Reads docs/releases/2026-10-09-production-acceptance.md (defects PA-xx, decisions Dx) and the
JSON results under .local/acceptance/e2e/ (regression, permissions, environments, inputs, auth,
data-source faults, product outages, golden paths). Writes one self-contained HTML page
(default .local/acceptance/report/redstring-acceptance.html) for publishing as an Artifact.
Status of each defect is derived from its register text; STATUS_OVERRIDES pins the few rows
whose text predates their verification.
"""
import argparse
import html
import json
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LOCAL = ROOT / '.local/acceptance'
REGISTER = ROOT / 'docs/releases/2026-10-09-production-acceptance.md'
E2E = LOCAL / 'e2e'

# verified · partial (backend done, frontend pending, or part of the scope) · open · later (next round) · gap
STATUS_OVERRIDES = {
    'PA-05': 'verified', 'PA-08': 'verified', 'PA-09': 'verified', 'PA-12': 'verified', 'PA-19': 'verified', 'PA-68': 'verified',
    'PA-21': 'verified', 'PA-43': 'verified', 'PA-84': 'verified', 'PA-85': 'verified', 'PA-86': 'verified', 'PA-87': 'verified', 'PA-83': 'verified', 'PA-67': 'verified', 'PA-72': 'verified', 'PA-73': 'verified', 'PA-74': 'verified',
    'PA-78': 'verified', 'PA-79': 'verified', 'PA-80': 'verified', 'PA-81': 'verified', 'PA-82': 'verified',
    'PA-88': 'verified', 'PA-89': 'verified', 'PA-90': 'verified',
}
STATUS_LABEL = {'verified': '已验证', 'partial': '部分完成', 'open': '处理中', 'later': '后续轮次', 'gap': '缺能力'}

PHASES = [
    ('0', '环境与数据接入', 'done', '两个独立实例（生产级、非生产级）、独立 PG、TLS 代理、四个角色账号；Prometheus、Loki、Tempo、Alertmanager、Kubernetes 清单与 DeepSeek 接入。'),
    ('1', '全页面对拍', 'done', '54 个静态路由 × 2 实例 × 中英文/深浅色共 216 次加载。首轮 0 页干净，修复后 210–212 页干净；剩余为预期的“配置仓库未接入”和 PA-70。'),
    ('2', '黄金路径', 'done', '登录与会话、告警闭环、调查画布、对话、知识、Agent 运行台、集成中心、设置均端到端走通；变更与发布当时按 D11 记为缺来源，已在阶段 8 补齐。'),
    ('3', '权限、隔离、故障、边界', 'done', '权限矩阵 130 条路由 × 4 角色 0 不一致；环境隔离 25/25；输入边界 12/12；登录与会话 10/10；五个数据源停机与四类产品故障都明确降级并自动恢复。'),
    ('4', '实验集群故障演练', 'done', '坏发布、重试风暴、Pod OOM、Pod 崩溃、消费者停止、节点压力六个场景：注入 → 告警 → 产品 → AI 取证 → 恢复，修复后重跑。'),
    ('5', '修复后全量回归', 'done', '基线、第二轮与回滚部署后的回归均已完成；最终一轮见下方“回归”数字。'),
    ('6', 'Kubernetes 回滚闭环', 'done', '下一轮第一段（用户 10-10 批准）：实验 Vault 凭据、独立执行器、两人通行密钥批准、恢复验证、AI 回滚建议与画布卡片；prod 端到端与 nonprod 五个失败与边界场景都通过。实验虚拟机时钟跳变（D12、D16）查明为 guest 内核缺少计数器倒退保护，经用户批准换为 HWE 7.0.0-38 并加自动修复守护，换后复验 23/23。'),
    ('7', '统一来源登记第二阶段', 'done', '用户 10-10 批准，逐个读取方切换（覆盖度、服务映射、日志、链路、AI 指标工具、映射写入、能力标签），每步先 nonprod 后 prod、切换前后对照；之后缺省改为 unified 并删除开关，回退演练、回滚演练 23/23 与完整回归 15/15 均通过；期间修复链路页 PA-78～82。P4 清理（用户 10-10 批准）已完成：旧回滚计划到期迁移、V3 实例下线旧动作路由、取消 legacy/compare 模式、V3 不再读遥测文件与旧 Kubernetes 登记；部署后回滚演练 23/23、完整回归 15/15。'),
    ('8', 'D11 交付链路', 'done', '用户 10-11 批准设计与设计图：实验 GitLab 经 TLS 代理接入两个实例，“变更与发布”和事件“告警前后变更”按服务读部署、流水线、合并请求并对上集群修订；AI 新增 `read_service_changes`；“启用告警规则”改由独立执行器在两人通行密钥批准后提交、fast-forward 合并、经 git-sync 与 Prometheus 自动重载生效并读回，另做停用。完整发布演练第 2 轮 24/24（坏版本经 GitLab 上线 → 告警 → 事件列出部署 → 从事件发起调查 → AI 建议回滚 → 执行器回滚 → 经 GitLab 修复）；反例逐项符合设计。期间修复 PA-88～90。'),
    ('9', 'D11 收尾与配置提升', 'done', '用户 10-12 要求除性能与 24 小时外全部收尾：GitLab 与产品逐条比对（两实例 6/6，入回归）、告警规则闭环补待生效列表、MR 与 main 文件、审计检查，D11 页面对照设计图核对；配置提升首次接实验配置仓库 `aiops-lab/redstring-config`（私有 CA、Ed25519 签名、真人审批合并），画布模板、Runbook、告警规则、Agent 四类从 nonprod 导出到 prod 导入全部走通，Runbook 首次走通审核发布；审阅页批准后状态按用户确认的方案 A 实现。期间修复 PA-91～103（含两个 P1：跨实例按应用 key 对应、生产端导入入口）。'),
]

# D11 delivery chain (design docs/plans/2026-10-11-d11-delivery-chain.md §9): report file, scenario, expected.
D11 = [
    ('alert-rule-prod.json', 'prod · 启用与停用告警规则', '提案 → 两人通行密钥批准 → 执行器提交并合并 → Prometheus 读回 → 告警进入 prod 并归 checkout-api → 停用后规则消失、告警恢复'),
    ('alert-rule-nonprod.json', 'nonprod · 启用与停用告警规则', '同上（test 环境告警按设计由 Alertmanager 丢弃）；每轮回归都跑'),
    ('delivery-prod-round1.json', 'prod · 完整发布演练（第 1 轮）', '发现 PA-88（从事件发起调查 501）、PA-89（AI 未给结构化建议）与实验修复发布失败'),
    ('delivery-prod.json', 'prod · 完整发布演练（第 2 轮）', '坏版本经 GitLab 上线 → 告警 → 事件列出部署 → 从事件发起调查 → AI 读变更与修订并建议回滚 → 两人批准回滚 → 经 GitLab 修复'),
    ('d11-negatives-changes.json', 'prod · 变更来源反例', 'GitLab 暂停 503 source_unavailable、AI 如实说明源不可用；Vault 键失效 credential_unavailable；非 GitLab 变更无流水线'),
    ('d11-negatives-rules.json', 'nonprod · 告警规则反例', '单票、发起人、API 令牌不能批准；main 前移 needs_attention 不写入；同步停止加载超时后重新核对；GitLab 中途断开只读对账、只一个 MR'),
    ('../config-sync.json', 'nonprod → prod · 配置提升（各检查取最近一次）', '四类真实对象导出成 MR → lab-maintainer 审批合并 → prod 导入并逐类核对；旧清单要求重导、运维不能查看与导入、重放收敛'),
]

# Rollback drills (PA-08/72/73): report file, scenario, what should happen.
ROLLBACK = [
    ('rollback-prod-ui.json', 'prod · 从画布 AI 建议发起', '坏发布 → AI 读修订并建议 → 画布卡片打开抽屉预选修订 → 提交 → 两人通行密钥批准 → 执行 → 验证恢复'),
    ('rollback-prod.json', 'prod · 接口发起（含 AI 建议）', '同上，提案走接口；含 428/422、幂等重放、发起人与 API 令牌不能批准'),
    ('rollback-edge-faulty-target.json', 'nonprod · 回滚到带故障修订', '稳定期后验证失败，只写一次、不再写'),
    ('rollback-edge-restart.json', 'nonprod · 验证中重启进程', '新进程接管后仍成功，空档不计入观察时长'),
    ('rollback-edge-drift.json', 'nonprod · 批准前对象被改', 'needs_attention/drift，不写入'),
    ('rollback-edge-revoked.json', 'nonprod · 两票之间撤销审批人', '首票作废，不执行'),
    ('rollback-edge-write-forbidden.json', 'nonprod · 写入超出实验账号 RBAC', 'Kubernetes 拒绝，needs_attention/write_forbidden，未改动'),
]

DRILLS = [
    ('坏发布导致 5xx', 'LabHigh5xxRate', '约 60', '约 60', '静默 → 两人通行密钥批准 → 执行器写入 → 关闭演练后恢复', 'AI 判定 checkout-api 注入 500、order-api 为下游（trace_id 对应）'),
    ('重试风暴', 'LabHigh5xxRate', '53 / 63', '73', '关闭后约 2 分钟清除', '修复前 15 次取证后超预算失败；修复后完成，识别重试叠加嵌套超时'),
    ('Pod OOM', 'LabAPIDown', '183 / 193', '263 / 203', '关闭后 20–80 秒清除', '修复前失败；接入 kube-state 后定位 OOMKilled 的具体 Pod 与节点'),
    ('Pod 崩溃', 'LabAPIDown', '61', '71', '等下一次重启，约 100 秒', '修复前输出超长全丢（PA-46）'),
    ('消费者停止', 'LabMessageConsumptionNotProgressing', '253 / 273', '263 / 283', '关闭后 14–23 秒', '判定消费被主动暂停；接入 kafka_exporter 后读出积压 56 条、0.2 条/秒'),
    ('节点资源压力', 'LabVMNodeMemoryPressure', '102 / 122', '113 / 132', '关闭后 20 秒；静默中靠对账恢复（PA-47）', '最终一轮先列服务，定位 worker1 饱和、业务无实质影响'),
]

LATER = [
    ('缺陷发现尚未收敛', '10-12 仍新增 PA-88～104 共 17 个（3 个 P1），首次用真实数据跑的路径几乎都出问题。后续按日常方式实际使用一段时间，以连续无 P0/P1 为收敛标准。'),
    ('性能压测与连续 24 小时运行', '计划阶段即后置，下一轮。输入（D45）：服务目录 `/services` 在多页调用、每次向来源扇出，对拍并发加宿主高负载时触及 35 秒预算。'),
    ('验证环境的广度', '只验过一个实验集群、三个服务、实验 GitLab、DeepSeek 一个模型、单机单进程；多服务与大数据量、多副本部署、其他版本的 GitLab/Prometheus 未验证。部署到现有业务库需要用户另行授权。'),
    ('安全', '登录二次验证未做（PA-06 安全待办）；比只读更小的成员角色（D38：受邀成员都带旧版只读角色，自定义角色只能叠加）；本轮验收标准未单列安全测试，需一次独立安全审查。'),
    ('运维', '备份恢复、升级迁移、Redstring 自身的监控告警未验收；凭据轮换目前靠手工。'),
]

RERUN = [
    ('一轮回归', 'python3 tools/acceptance/regress.py --with-golden-api'),
    ('权限矩阵', 'python3 tools/acceptance/permissions.py prod'),
    ('环境、输入、登录', 'python3 tools/acceptance/environments.py && python3 tools/acceptance/inputs.py && python3 tools/acceptance/auth.py'),
    ('黄金路径（API）', 'python3 tools/acceptance/golden.py canvas|conversation|knowledge|settings|agents'),
    ('数据源停机', 'python3 tools/acceptance/faults.py loki|tempo|prometheus|elasticsearch|alertmanager'),
    ('产品故障', 'python3 tools/acceptance/outage.py model|database|restart-midstream|kubernetes'),
    ('故障演练', 'python3 tools/acceptance/drill.py <drill> --alerts … --question "…"'),
]


def esc(text):
    return html.escape(str(text), quote=True)


def inline(text):
    """Escape, then render `code` spans from the register's Markdown."""
    out = esc(text)
    return re.sub(r'`([^`]+)`', r'<code>\1</code>', out)


def defects():
    rows = []
    for line in REGISTER.read_text().splitlines():
        m = re.match(r'^\| (PA-\d+) \| ([^|]+) \| (.*) \| (.*) \|\s*$', line)
        if not m:
            continue
        pid, sev, problem, status = (x.strip() for x in m.groups())
        if pid in STATUS_OVERRIDES:
            state = STATUS_OVERRIDES[pid]
        elif sev == '缺能力':
            state = 'gap'
        elif '前端处理中' in status or '部分修复' in status:
            state = 'partial'
        elif '验收已验证' in status or re.match(r'^(已修复|后端已修复|后端已补|重新定性并修复|已修复为如实显示|已实现|前端已修)', status):
            state = 'verified'
        elif re.match(r'^(用户决定|方案已定)', status) and '已实现' in status:
            state = 'verified'
        else:
            state = 'open'
        rows.append({'id': pid, 'sev': sev, 'problem': problem, 'status': status, 'state': state})
    return rows


def decisions():
    out = []
    for line in REGISTER.read_text().splitlines():
        m = re.match(r'^\| (D\d+) \| ([^|]+) \| (.*) \| (.*) \|\s*$', line)
        if m:
            out.append([x.strip() for x in m.groups()])
    return sorted(out, key=lambda d: int(d[0][1:]))


def load(path, default=None):
    try:
        return json.loads(Path(path).read_text())
    except (OSError, ValueError):
        return default


def latest_regression():
    files = sorted(E2E.glob('regress-*.json'), key=lambda p: p.stat().st_mtime)
    return load(files[-1]) if files else None


def git(*args):
    return subprocess.run(['git', *args], cwd=ROOT, capture_output=True, text=True).stdout.strip()


def chip(state):
    return f'<span class="chip s-{state}">{STATUS_LABEL[state]}</span>'


def sev_chip(sev):
    cls = {'P0': 'p0', 'P1': 'p1', 'P2': 'p2', 'P3': 'p3'}.get(sev, 'pg')
    return f'<span class="sev {cls}">{esc(sev)}</span>'


def build():
    rows = defects()
    counts = {k: sum(1 for r in rows if r['state'] == k) for k in STATUS_LABEL}
    sev_open = {s: sum(1 for r in rows if r['sev'] == s and r['state'] in ('open', 'partial')) for s in ('P0', 'P1', 'P2', 'P3')}
    reg = latest_regression()
    perm = {i: load(E2E / f'permissions-{i}.json', {}) for i in ('prod', 'nonprod')}
    env = load(E2E / 'environments.json', [])
    inputs = load(E2E / 'inputs.json', [])
    auth = load(E2E / 'auth.json', [])
    faults = {n: load(E2E / f'faults/{n}.json') for n in ('loki', 'tempo', 'prometheus', 'elasticsearch', 'alertmanager')}
    outages = {n: load(E2E / f'outages/{n}.json') for n in ('model', 'database', 'restart-midstream', 'kubernetes')}
    golden = {n: load(E2E / f'golden/{n}.json', []) for n in ('canvas', 'conversation', 'knowledge', 'settings', 'agents')}
    commit = git('rev-parse', '--short', 'HEAD')
    branch = git('rev-parse', '--abbrev-ref', 'HEAD')

    def passed(items):
        return f"{sum(1 for x in items if x.get('ok'))}/{len(items)}" if items else '—'

    perm_line = []
    for inst, data in perm.items():
        results = data.get('results') or []
        bad = [r for r in results if not r.get('ok') and not r.get('inconclusive')]
        perm_line.append((inst, len(results), len(bad)))

    p = []
    p.append(f'''<title>Redstring 生产验收</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=Noto+Sans+SC:wght@400;500;700&family=Noto+Serif+SC:wght@600;700&display=swap">
<style>
/* Layout: one reading column; a thin red thread runs down the phase list and knots at each phase. */
:root {{
  --paper: #f5f6f8; --sheet: #ffffff; --ink: #1d232c; --muted: #5d6673; --rule: #dfe3e8;
  --thread: #b3262d; --thread-soft: #f3dcdd;
  --ok: #1f7a5a; --ok-bg: #e2f2ea; --warn: #9a6200; --warn-bg: #f8edd8; --bad: #b4232f; --bad-bg: #f9e1e3;
  --info: #3b5b8c; --info-bg: #e3eaf4; --none: #6b7280; --none-bg: #eceef1;
  --display: "Noto Serif SC", "Songti SC", serif; --body: "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif;
  --mono: "IBM Plex Mono", ui-monospace, "SFMono-Regular", Menlo, monospace;
}}
@media (prefers-color-scheme: dark) {{ :root:not([data-theme="light"]) {{
  --paper: #13161b; --sheet: #1a1e25; --ink: #e6e9ee; --muted: #9aa3af; --rule: #2c323b;
  --thread: #e2565d; --thread-soft: #3a2024;
  --ok: #5cc79d; --ok-bg: #173229; --warn: #e2b04f; --warn-bg: #352a14; --bad: #f07a83; --bad-bg: #3a1c20;
  --info: #8fb0e3; --info-bg: #1d2a3d; --none: #a3aab5; --none-bg: #262b33; color-scheme: dark; }} }}
:root[data-theme="dark"] {{
  --paper: #13161b; --sheet: #1a1e25; --ink: #e6e9ee; --muted: #9aa3af; --rule: #2c323b;
  --thread: #e2565d; --thread-soft: #3a2024;
  --ok: #5cc79d; --ok-bg: #173229; --warn: #e2b04f; --warn-bg: #352a14; --bad: #f07a83; --bad-bg: #3a1c20;
  --info: #8fb0e3; --info-bg: #1d2a3d; --none: #a3aab5; --none-bg: #262b33; color-scheme: dark; }}
body {{ background: var(--paper); color: var(--ink); font: 15px/1.7 var(--body); }}
.wrap {{ max-width: 1080px; margin: 0 auto; padding: 40px 20px 72px; display: grid; gap: 44px; }}
.wrap > *, section > *, .two > * {{ min-width: 0; }}
header {{ display: grid; gap: 10px; }}
.brand {{ display: flex; align-items: center; gap: 10px; font: 500 12px/1 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--muted); }}
.brand svg {{ width: 18px; height: 18px; }}
h1 {{ font: 700 clamp(28px, 4vw, 40px)/1.2 var(--display); margin: 0; text-wrap: balance; }}
h2 {{ font: 700 22px/1.3 var(--display); margin: 0; text-wrap: balance; }}
h3 {{ font: 700 15px/1.4 var(--body); margin: 0; }}
.meta {{ display: flex; flex-wrap: wrap; gap: 6px 18px; color: var(--muted); font-size: 13px; }}
.meta code, code {{ font: 13px var(--mono); overflow-wrap: anywhere; }}
p {{ margin: 0; max-width: 68ch; }}
section {{ display: grid; gap: 16px; }}
.lede {{ font-size: 16px; display: grid; gap: 10px; }}
.figures {{ display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px; }}
.fig {{ background: var(--sheet); border: 1px solid var(--rule); border-radius: 10px; padding: 14px 16px; display: grid; gap: 2px; }}
.fig b {{ font: 600 28px/1.1 var(--mono); font-variant-numeric: tabular-nums; }}
.fig span {{ color: var(--muted); font-size: 13px; }}
.fig.alert b {{ color: var(--bad); }}
.phases {{ list-style: none; margin: 0; padding: 0 0 0 28px; position: relative; display: grid; gap: 18px; }}
.phases::before {{ content: ""; position: absolute; left: 7px; top: 8px; bottom: 8px; width: 2px; background: var(--thread); border-radius: 2px; }}
.phases li {{ position: relative; display: grid; gap: 4px; }}
.phases li::before {{ content: ""; position: absolute; left: -27px; top: 6px; width: 14px; height: 14px; border-radius: 50%; background: var(--sheet); border: 3px solid var(--thread); box-sizing: border-box; }}
.phases li.running::before {{ background: var(--thread-soft); border-style: dashed; }}
.phases .head {{ display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; }}
.phases .num {{ font: 500 12px var(--mono); color: var(--thread); }}
.chip, .sev {{ display: inline-block; font: 500 12px/1.6 var(--body); padding: 0 8px; border-radius: 999px; white-space: nowrap; }}
.s-verified {{ color: var(--ok); background: var(--ok-bg); }}
.s-partial {{ color: var(--warn); background: var(--warn-bg); }}
.s-open {{ color: var(--bad); background: var(--bad-bg); }}
.s-later, .s-gap {{ color: var(--none); background: var(--none-bg); }}
.s-done {{ color: var(--ok); background: var(--ok-bg); }} .s-running {{ color: var(--info); background: var(--info-bg); }}
.sev {{ font-family: var(--mono); border: 1px solid currentColor; padding: 0 6px; }}
.p0 {{ color: var(--bad); }} .p1 {{ color: var(--thread); }} .p2 {{ color: var(--warn); }} .p3, .pg {{ color: var(--none); }}
.panel {{ background: var(--sheet); border: 1px solid var(--rule); border-radius: 10px; }}
.scroll {{ overflow-x: auto; }}
table {{ width: 100%; border-collapse: collapse; font-size: 14px; }}
th, td {{ text-align: left; vertical-align: top; padding: 10px 14px; border-bottom: 1px solid var(--rule); }}
th {{ font: 500 12px var(--body); letter-spacing: .04em; color: var(--muted); white-space: nowrap; }}
tr:last-child td {{ border-bottom: 0; }}
td.num {{ font-family: var(--mono); font-variant-numeric: tabular-nums; white-space: nowrap; }}
.filters {{ display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }}
.filters button {{ font: 13px var(--body); color: var(--ink); background: var(--sheet); border: 1px solid var(--rule); border-radius: 999px; padding: 3px 12px; cursor: pointer; }}
.filters button[aria-pressed="true"] {{ border-color: var(--thread); color: var(--thread); }}
.filters button:focus-visible, summary:focus-visible {{ outline: 2px solid var(--thread); outline-offset: 2px; }}
.defects {{ display: grid; }}
.defect {{ display: grid; grid-template-columns: 72px 52px 82px minmax(0, 1fr); gap: 4px 12px; padding: 12px 16px; border-bottom: 1px solid var(--rule); align-items: start; }}
.defect:last-child {{ border-bottom: 0; }}
.defect .id {{ font: 500 13px/1.9 var(--mono); }}
.defect .text {{ display: grid; gap: 4px; min-width: 0; }}
.defect details {{ color: var(--muted); font-size: 13px; }}
.defect summary {{ cursor: pointer; width: fit-content; }}
.defect details p {{ max-width: none; padding-top: 4px; overflow-wrap: anywhere; }}
.two {{ display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 16px; }}
.kv {{ display: grid; gap: 8px; padding: 16px; }}
.kv div {{ display: flex; justify-content: space-between; gap: 12px; border-bottom: 1px dashed var(--rule); padding-bottom: 6px; }}
.kv div:last-child {{ border-bottom: 0; }}
.kv b {{ font: 500 14px var(--mono); font-variant-numeric: tabular-nums; white-space: nowrap; }}
.note {{ color: var(--muted); font-size: 13px; }}
.later {{ display: grid; gap: 10px; margin: 0; padding: 0; list-style: none; }}
.later li {{ display: grid; gap: 2px; }}
pre {{ margin: 0; padding: 14px 16px; overflow-x: auto; font: 13px/1.7 var(--mono); }}
footer {{ color: var(--muted); font-size: 13px; }}
@media (max-width: 640px) {{
  .defect {{ grid-template-columns: auto auto 1fr; }}
  .defect .text {{ grid-column: 1 / -1; }}
}}
</style>
''')
    p.append('<div class="wrap">')
    p.append(f'''<header>
  <div class="brand"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M13 2 4 14h7l-1 8 9-12h-7l1-8z" fill="var(--thread)"/></svg>Redstring · AIOps</div>
  <h1>生产验收报告</h1>
  <div class="meta"><span>2026-10-09 至 10-12</span><span>分支 <code>{esc(branch)}</code></span><span>提交 <code>{esc(commit)}</code></span><span>实验集群 <code>aiops-vm-cluster</code>（三台 Lima 虚拟机）</span></div>
</header>''')

    open_p01 = sev_open['P0'] + sev_open['P1']
    p.append(f'''<section class="lede">
  <h2>结论</h2>
  <p>{VERDICT}</p>
  <div class="figures">
    <div class="fig"><b>{len(rows)}</b><span>登记缺陷与缺口</span></div>
    <div class="fig"><b>{counts['verified']}</b><span>已修复并在实例上验证</span></div>
    <div class="fig{' alert' if counts['open'] else ''}"><b>{counts['open'] + counts['partial']}</b><span>处理中或部分完成</span></div>
    <div class="fig{' alert' if open_p01 else ''}"><b>{open_p01}</b><span>未关闭的 P0/P1</span></div>
    <div class="fig"><b>{counts['later'] + counts['gap']}</b><span>转入后续轮次或缺能力</span></div>
  </div>
</section>''')

    p.append('<section><h2>阶段</h2><ol class="phases">')
    for num, name, state, text in PHASES:
        label = '完成' if state == 'done' else '进行中'
        p.append(f'<li class="{state}"><div class="head"><span class="num">阶段 {num}</span><h3>{esc(name)}</h3>'
                 f'<span class="chip s-{state}">{label}</span></div><p>{inline(text)}</p></li>')
    p.append('</ol></section>')

    # Defects
    p.append('<section><h2>缺陷清单</h2><div class="filters" role="group" aria-label="按状态筛选">')
    p.append('<button type="button" data-f="all" aria-pressed="true">全部</button>')
    for k, v in STATUS_LABEL.items():
        if counts[k]:
            p.append(f'<button type="button" data-f="{k}" aria-pressed="false">{v} {counts[k]}</button>')
    p.append('</div><div class="panel defects" id="defects">')
    for r in rows:
        p.append(f'''<div class="defect" data-state="{r['state']}"><span class="id">{r['id']}</span>{sev_chip(r['sev'])}{chip(r['state'])}
  <div class="text"><span>{inline(r['problem'])}</span><details><summary>处理与证据</summary><p>{inline(r['status'])}</p></details></div></div>''')
    p.append('</div></section>')

    # Drills
    p.append('<section><h2>故障演练</h2><p class="note">时间为开启演练后的秒数；两个数字分别为修复前与修复后两轮。</p><div class="panel scroll"><table><thead><tr>'
             '<th>场景</th><th>告警</th><th>触发 s</th><th>进入产品 s</th><th>恢复</th><th>AI 取证</th></tr></thead><tbody>')
    for name, alert, fire, product, recover, ai in DRILLS:
        p.append(f'<tr><td>{esc(name)}</td><td><code>{esc(alert)}</code></td><td class="num">{esc(fire)}</td>'
                 f'<td class="num">{esc(product)}</td><td>{esc(recover)}</td><td>{esc(ai)}</td></tr>')
    p.append('</tbody></table></div></section>')

    # Rollback loop
    p.append('<section><h2>Kubernetes 回滚闭环</h2><p class="note">实验集群 aiops-lab；写入只经独立执行器和两人通行密钥批准，'
             '写令牌只在 Vault；结果取自 <code>.local/acceptance/e2e/drills/rollback-*.json</code>。</p><div class="panel scroll"><table><thead><tr>'
             '<th>场景</th><th>应有结果</th><th>执行状态</th><th>检查</th></tr></thead><tbody>')
    for fname, name, expect in ROLLBACK:
        d = load(E2E / 'drills' / fname)
        if not d:
            p.append(f'<tr><td>{esc(name)}</td><td>{esc(expect)}</td><td>—</td><td>未运行</td></tr>')
            continue
        ex = d.get('execution') or {}
        state = ' / '.join(x for x in (ex.get('status'), ex.get('status_reason')) if x) or '未执行'
        ok = sum(1 for c in d.get('checks', {}).values() if c.get('ok'))
        p.append(f'<tr><td>{esc(name)}</td><td>{esc(expect)}</td><td><code>{esc(state)}</code></td>'
                 f'<td class="num">{ok}/{len(d.get("checks", {}))}</td></tr>')
    p.append('</tbody></table></div></section>')

    # D11
    p.append('<section><h2>D11 交付链路</h2><p class="note">实验 GitLab（aiops-lab/checkout-api、aiops-lab/prometheus-rules）与 aiops-vm-cluster；'
             '规则写入只经独立执行器和两人通行密钥批准；结果取自 <code>.local/acceptance/e2e/drills/</code>。</p><div class="panel scroll"><table><thead><tr>'
             '<th>场景</th><th>应有结果</th><th>检查</th></tr></thead><tbody>')
    for fname, name, expect in D11:
        d = load(E2E / 'drills' / fname)
        if not d:
            p.append(f'<tr><td>{esc(name)}</td><td>{esc(expect)}</td><td>未运行</td></tr>')
            continue
        items = list(d['checks'].values()) if isinstance(d.get('checks'), dict) else d.get('results') or []
        # A file that accumulates runs (config-sync.json) counts each check's latest result.
        if items and 'check' in items[0]:
            items = list({c['check']: c for c in items}.values())
        ok = sum(1 for c in items if c.get('ok'))
        p.append(f'<tr><td>{esc(name)}</td><td>{esc(expect)}</td><td class="num">{ok}/{len(items)}</td></tr>')
    p.append('</tbody></table></div></section>')

    # Phase 3
    p.append('<section><h2>权限、隔离与故障恢复</h2><div class="two">')
    p.append('<div class="panel kv"><h3>检查</h3>')
    for inst, total, bad in perm_line:
        p.append(f'<div><span>权限矩阵 {inst}（探测次数 / 不一致）</span><b>{total} / {bad}</b></div>')
    p.append(f'<div><span>环境隔离</span><b>{passed(env)}</b></div><div><span>输入边界</span><b>{passed(inputs)}</b></div>'
             f'<div><span>登录与会话</span><b>{passed(auth)}</b></div></div>')
    p.append('<div class="panel kv"><h3>数据源停机（停机时 → 恢复）</h3>')
    for name, data in faults.items():
        if not data:
            continue
        codes = sorted({f"{v['status']}" + (f" {v['code']}" if v.get('code') else '') for v in data['degraded'].values()})
        ok = data.get('degraded_ok') and data.get('recovered_ok')
        p.append(f'<div><span>{esc(name)}：{esc(", ".join(codes))}</span><b>{"恢复 ✓" if ok else "需复核"}</b></div>')
    p.append('</div>')
    p.append('<div class="panel kv"><h3>产品故障</h3>')
    labels = {'model': '模型不可达', 'database': '数据库停 20 秒', 'restart-midstream': '流式输出中重启后端', 'kubernetes': 'Kubernetes 采集不可达'}
    for name, data in outages.items():
        if data:
            p.append(f'<div><span>{labels[name]}</span><b>{"通过" if data.get("ok") else "未通过"}</b></div>')
    p.append('<p class="note">Kubernetes 采集超过 5 分钟后拓扑变成无提示空图，见 PA-61。</p></div>')
    p.append('<div class="panel kv"><h3>黄金路径（API 步骤）</h3>')
    names = {'canvas': '调查画布', 'conversation': '对话', 'knowledge': '知识', 'settings': '设置', 'agents': 'Agent 运行台'}
    for name, steps in golden.items():
        if steps:
            p.append(f'<div><span>{names[name]}</span><b>{passed(steps)}</b></div>')
    p.append('<p class="note">另有 Playwright 用例：对话、邀请、静默抽屉提议与通行密钥批准。</p></div>')
    p.append('</div>')
    if reg:
        p.append(f'<p class="note">最近一轮回归（{esc(reg.get("tag"))}，{esc(reg.get("at"))}，提交 <code>{esc(reg.get("commit"))}</code>）：'
                 f'{sum(1 for r in reg["results"] if r.get("ok"))}/{len(reg["results"])} 项通过。</p>')
    p.append('</section>')

    # Decisions
    p.append('<section><h2>自主决定（待你确认）</h2><p class="note">你离开期间按“需要选择时先做、留下记录”处理的决定。</p>'
             '<div class="panel scroll"><table><thead><tr><th>编号</th><th>时间</th><th>决定</th><th>理由</th></tr></thead><tbody>')
    for d in decisions():
        p.append(f'<tr><td class="num">{esc(d[0])}</td><td class="num">{esc(d[1])}</td><td>{inline(d[2])}</td><td>{inline(d[3])}</td></tr>')
    p.append('</tbody></table></div></section>')

    p.append('<section><h2>不在本轮</h2><ul class="later">')
    for name, why in LATER:
        p.append(f'<li><h3>{esc(name)}</h3><span class="note">{esc(why)}</span></li>')
    p.append('</ul></section>')

    p.append('<section><h2>复现</h2><div class="panel"><pre>')
    p.append('\n'.join(f'# {esc(name)}\n{esc(cmd)}' for name, cmd in RERUN))
    p.append('</pre></div><p class="note">完整记录：<code>docs/releases/2026-10-09-production-acceptance.md</code>；'
             '计划：<code>docs/plans/2026-10-09-production-acceptance.md</code>。</p></section>')
    p.append(f'<footer>由 <code>tools/acceptance/report.py</code> 根据缺陷清单与运行结果生成。</footer></div>')
    p.append('''<script>
(() => {
  const buttons = document.querySelectorAll('.filters button');
  const rows = document.querySelectorAll('#defects .defect');
  buttons.forEach((b) => b.addEventListener('click', () => {
    buttons.forEach((x) => x.setAttribute('aria-pressed', String(x === b)));
    rows.forEach((r) => { r.hidden = b.dataset.f !== 'all' && r.dataset.state !== b.dataset.f; });
  }));
})();
</script>''')
    return '\n'.join(p)


VERDICT = ('本轮在两个独立验收实例和三节点实验集群上完成了功能、权限和故障三个层面的验收。'
           '主流程（登录、告警进入产品、AI 只读取证、调查画布、静默提案经两人通行密钥批准后由执行器写入、知识与 Agent 治理）都已端到端走通，'
           '权限矩阵与环境隔离没有越权。下一轮第一段的 Kubernetes 回滚闭环也已在实验集群上验证：AI 读修订并建议、人从画布提交、'
           '两人通行密钥批准、独立执行器写入并验证恢复，五个失败与边界场景都按设计停下、没有误写。'
           '统一来源登记第二阶段也已完成：集成中心登记的连接器成为服务映射、日志、链路、AI 取证和回滚的唯一来源，缺省改为 unified 后回滚演练与完整回归均通过。'
           'P4 清理也已完成，V3 实例只读统一登记。D11 交付链路也已接通：GitLab 变更与集群修订对上，告警前后变更进入事件与 AI 取证，'
           '告警规则经独立执行器启用与停用，完整发布演练从坏版本上线到回滚、修复全程通过。配置提升也已接上真实配置仓库：四类配置从非生产导出、经人工审批合并后在生产导入。性能与 24 小时稳定性放到下一轮。'
           '用户 10-12 的定位：beta——作为 MVP 与实验环境演示可用，尚不算生产可用；距生产可用的五项见下方“不在本轮”。')


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--out', default=str(LOCAL / 'report/redstring-acceptance.html'))
    a = ap.parse_args()
    out = Path(a.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(build())
    rows = defects()
    print(f'{out} · {len(rows)} defects · ' + ', '.join(f"{v} {sum(1 for r in rows if r['state'] == k)}" for k, v in STATUS_LABEL.items()))


if __name__ == '__main__':
    main()
