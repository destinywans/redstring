// Image review gallery only. No product operations or data connections.
const pages = [{"id": "home", "group": "工作台", "title": "工作台", "route": "/", "prompt": "prompts/home.txt"}, {"id": "agent-sessions", "group": "Agent", "title": "会话与画布", "route": "/investigations", "prompt": "prompts/agent-sessions.txt"}, {"id": "agent-memory", "group": "Agent", "title": "记忆与上下文", "route": "/memory", "prompt": "prompts/agent-memory.txt"}, {"id": "agent-skills", "group": "Agent", "title": "Skills", "route": "/ai-extensions/skills", "prompt": "prompts/agent-skills.txt"}, {"id": "agent-prompts", "group": "Agent", "title": "Prompt 模板", "route": "/ai-extensions/prompts", "prompt": "prompts/agent-prompts.txt"}, {"id": "agent-tasks", "group": "Agent", "title": "自动化任务", "route": "/settings", "prompt": "prompts/agent-tasks-revision.txt"}, {"id": "resources-applications", "group": "资源中心", "title": "全部应用", "route": "/applications", "prompt": "prompts/resources-applications.txt"}, {"id": "resources-catalog", "group": "资源中心", "title": "资源目录", "route": "/resources/catalog", "prompt": "prompts/resources-catalog.txt"}, {"id": "resources-relations", "group": "资源中心", "title": "资源关系", "route": "/resources/relations", "prompt": "prompts/resources-relations-revision.txt"}, {"id": "delivery-paused", "group": "交付中心", "title": "交付中心", "route": "/delivery", "prompt": "prompts/delivery-paused-revision.txt"}, {"id": "infra-overview", "group": "基础设施", "title": "资源总览", "route": "/infrastructure?view=summary", "prompt": "prompts/infra-overview.txt"}, {"id": "infra-clusters", "group": "基础设施", "title": "集群", "route": "/infrastructure?view=clusters", "prompt": "prompts/infra-clusters.txt"}, {"id": "infra-nodes", "group": "基础设施", "title": "节点", "route": "/infrastructure?view=nodes", "prompt": "prompts/infra-nodes.txt"}, {"id": "infra-workloads", "group": "基础设施", "title": "工作负载", "route": "/infrastructure?view=workloads", "prompt": "prompts/infra-workloads.txt"}, {"id": "infra-pods", "group": "基础设施", "title": "Pod 与容器", "route": "/infrastructure?view=pods", "prompt": "prompts/infra-pods.txt"}, {"id": "infra-events", "group": "基础设施", "title": "Kubernetes 事件", "route": "/infrastructure?view=events", "prompt": "prompts/infra-events.txt"}, {"id": "infra-namespaces", "group": "基础设施", "title": "命名空间", "route": "/infrastructure?view=namespaces", "prompt": "prompts/infra-namespaces.txt"}, {"id": "infra-hosts", "group": "基础设施", "title": "主机与虚拟机", "route": "/infrastructure?view=hosts", "prompt": "prompts/infra-hosts.txt"}, {"id": "infra-network", "group": "基础设施", "title": "网络", "route": "/infrastructure?view=network", "prompt": "prompts/infra-network.txt"}, {"id": "infra-storage", "group": "基础设施", "title": "存储", "route": "/infrastructure?view=storage", "prompt": "prompts/infra-storage.txt"}, {"id": "infra-middleware", "group": "基础设施", "title": "中间件与数据库", "route": "/infrastructure?view=middleware", "prompt": "prompts/infra-middleware.txt"}, {"id": "observ-metrics", "group": "可观测性", "title": "指标监控", "route": "/observability/metrics", "prompt": "prompts/observ-metrics.txt"}, {"id": "observ-logs", "group": "可观测性", "title": "日志检索", "route": "/observability/logs", "prompt": "prompts/observ-logs.txt"}, {"id": "observ-traces", "group": "可观测性", "title": "链路追踪", "route": "/observability/traces", "prompt": "prompts/observ-traces.txt"}, {"id": "observ-topology", "group": "可观测性", "title": "调用拓扑", "route": "/observability/topology", "prompt": "prompts/observ-topology.txt"}, {"id": "incidents", "group": "告警与事件", "title": "事件中心", "route": "/incidents", "prompt": "prompts/incidents.txt"}, {"id": "alerts", "group": "告警与事件", "title": "告警列表", "route": "/alerts", "prompt": "prompts/alerts.txt"}, {"id": "alert-sources", "group": "告警与事件", "title": "告警来源", "route": "/alerts/sources", "prompt": "prompts/alert-sources.txt"}, {"id": "knowledge-runbooks", "group": "知识库", "title": "Runbook", "route": "/knowledge", "prompt": "prompts/knowledge-runbooks.txt"}, {"id": "integrations-connectors", "group": "集成中心", "title": "连接器", "route": "/integrations?tab=tools", "prompt": "prompts/integrations-connectors.txt"}, {"id": "integrations-mcp", "group": "集成中心", "title": "MCP 服务", "route": "/ai-extensions/mcp", "prompt": "prompts/integrations-mcp.txt"}, {"id": "integrations-models", "group": "集成中心", "title": "模型服务", "route": "/integrations?tab=models", "prompt": "prompts/integrations-models-final.txt"}, {"id": "integrations-embedding", "group": "集成中心", "title": "Embedding 与检索", "route": "/integrations?tab=knowledge", "prompt": "prompts/integrations-embedding-final.txt"}, {"id": "audit", "group": "审计日志", "title": "审计日志", "route": "/audit", "prompt": "prompts/audit.txt"}, {"id": "settings-overview", "group": "设置", "title": "平台概览", "route": "/platform/settings", "prompt": "prompts/settings-overview.txt"}, {"id": "settings-members", "group": "设置", "title": "成员管理", "route": "/members", "prompt": "prompts/settings-members-revision.txt"}, {"id": "settings-teams", "group": "设置", "title": "团队管理", "route": "/teams", "prompt": "prompts/settings-teams.txt"}, {"id": "settings-roles", "group": "设置", "title": "角色与资源范围", "route": "/platform/roles", "prompt": "prompts/settings-roles.txt"}];
const groups = [...new Set(pages.map(page => page.group))];
const byId = new Map(pages.map(page => [page.id, page]));
let selectedGroup = '';
let search = '';
let current = null;
const content = document.querySelector('#pages');
const groupNav = document.querySelector('#groups');
const viewer = document.querySelector('#viewer');
const image = document.querySelector('#full-image');
const pathFor = page => 'images/' + page.id + '.png';
const el = (tag, className, text) => {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};
function visiblePages() {
  return pages.filter(page => (!selectedGroup || page.group === selectedGroup) && (page.title + page.group).toLowerCase().includes(search));
}
function renderGroups() {
  groupNav.replaceChildren();
  for (const group of ['', ...groups]) {
    const button = el('button', '', group || '全部页面');
    button.type = 'button';
    button.setAttribute('aria-pressed', String(group === selectedGroup));
    button.append(el('span', '', String(group ? pages.filter(page => page.group === group).length : pages.length)));
    button.addEventListener('click', () => { selectedGroup = group; renderGroups(); renderPages(); });
    groupNav.append(button);
  }
}
function renderPages() {
  content.replaceChildren();
  const visible = visiblePages();
  document.querySelector('#count').textContent = visible.length + ' / ' + pages.length + ' 张图';
  for (const group of groups) {
    const children = visible.filter(page => page.group === group);
    if (!children.length) continue;
    const section = el('section', 'page-group');
    section.append(el('h2', '', group));
    const cards = el('div', 'cards');
    for (const page of children) {
      const button = el('button', 'card');
      button.type = 'button';
      button.setAttribute('aria-label', '放大 ' + page.group + ' · ' + page.title);
      const preview = el('span', 'preview');
      const thumbnail = el('img');
      thumbnail.src = pathFor(page);
      thumbnail.alt = page.title + '页面设计概念图';
      thumbnail.loading = 'lazy';
      thumbnail.decoding = 'async';
      preview.append(thumbnail);
      const caption = el('span', 'card-meta');
      caption.append(el('span', 'card-number', String(pages.indexOf(page) + 1).padStart(2, '0')), el('span', 'card-title', page.title), el('span', 'card-open', '放大 ↗'));
      button.append(preview, caption);
      button.addEventListener('click', () => openPage(page.id));
      cards.append(button);
    }
    section.append(cards);
    content.append(section);
  }
  if (!visible.length) content.append(el('p', 'empty', '没有匹配的页面，请换个关键词或分类。'));
}
function openPage(id, setHash = true) {
  const page = byId.get(id);
  if (!page) return;
  current = page;
  image.src = pathFor(page);
  image.alt = page.group + ' · ' + page.title + '完整设计图';
  document.querySelector('#viewer-group').textContent = page.group + ' / 设计概念 · 示例数据';
  document.querySelector('#viewer-title').textContent = page.title;
  document.querySelector('#viewer-route').textContent = page.route;
  document.querySelector('#position').textContent = (pages.indexOf(page) + 1) + ' / ' + pages.length;
  document.querySelector('#original').href = pathFor(page);
  document.querySelector('#prompt').href = page.prompt || ('prompts/' + page.id + '.txt');
  if (!viewer.open) viewer.showModal();
  if (setHash) history.replaceState(null, '', '#' + page.id);
}
function closePage() {
  viewer.close();
  current = null;
  history.replaceState(null, '', location.pathname + location.search);
}
function step(delta) {
  if (!current) return;
  const index = (pages.indexOf(current) + delta + pages.length) % pages.length;
  openPage(pages[index].id);
}
document.querySelector('#search').addEventListener('input', event => { search = event.target.value.trim().toLowerCase(); renderPages(); });
document.querySelector('#prev').addEventListener('click', () => step(-1));
document.querySelector('#next').addEventListener('click', () => step(1));
document.querySelector('#close').addEventListener('click', closePage);
viewer.addEventListener('cancel', event => { event.preventDefault(); closePage(); });
viewer.addEventListener('click', event => { if (event.target === viewer) closePage(); });
document.addEventListener('keydown', event => {
  if (!viewer.open || event.altKey || event.ctrlKey || event.metaKey) return;
  if (event.key === 'ArrowLeft') { event.preventDefault(); step(-1); }
  if (event.key === 'ArrowRight') { event.preventDefault(); step(1); }
});
window.addEventListener('hashchange', () => { const id = location.hash.slice(1); if (byId.has(id)) openPage(id, false); });
renderGroups();
renderPages();
if (byId.has(location.hash.slice(1))) openPage(location.hash.slice(1), false);
