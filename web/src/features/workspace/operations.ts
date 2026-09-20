export type Incident = {
  id: string;
  source_id: string;
  application_id: string;
  environment_id: string;
  fingerprint: string;
  title: string;
  severity: string;
  status: string;
  alert_status: string;
  suspected: boolean;
  assignee_id: string;
  investigation_id: string;
  starts_at: string;
  ends_at?: string;
  updated_at: string;
  summary: string;
};
export type Todo = {
  id: string;
  application_id: string;
  environment_id?: string;
  investigation_id?: string;
  title: string;
  status: string;
  kind: string;
  updated_at: string;
};
export type AutomationRun = {
  id: string;
  incident_id?: string;
  result?: {
    state?: string;
    summary?: string;
    evidence?: {
      kind: string;
      title: string;
      content: string;
      source: string;
    }[];
  };
  environment_id: string;
  kind: string;
  status: string;
  investigation_id: string;
  run_id: string;
  error?: string;
  created_at: string;
  finished_at?: string;
};
export type Notification = {
  id: string;
  incident_id: string;
  channel: string;
  status: string;
  attempts: number;
  next_attempt_at: string;
  last_error: string;
  created_at: string;
};
export type Policy = {
  environment_id: string;
  auto_initial: boolean;
  patrol: boolean;
  model_id: string;
  actor_id: string;
  interval_minutes: number;
  max_rounds: number;
  max_tool_calls: number;
  timeout_seconds: number;
  daily_budget: number;
  generate_experience_draft: boolean;
  version: number;
  inherit?: boolean;
};
export type Dashboard = {
  incidents: Incident[];
  pending_approvals: Todo[];
  executions: Todo[];
  my_items: Todo[];
  automation_runs: AutomationRun[];
  notification_failures: Notification[];
  data_sources: {
    id?: string;
    name?: string;
    status?: string;
    error?: string;
  }[];
};
export const statusName = (value: string) =>
  ({
    open: "待处理",
    investigating: "调查中",
    resolved: "告警已恢复",
    firing: "触发中",
    pending: "待处理",
    not_configured: "未配置",
    configured_unverified: "已配置 · 未验证",
    unknown: "状态未知",
    retrying: "等待重试",
    sending: "发送中",
    sent: "已发送",
    failed: "失败",
    queued: "排队中",
    running: "运行中",
    completed: "已完成",
    succeeded: "成功",
    cancelled: "已取消",
    needs_attention: "需人工处理",
    indeterminate: "无法判定",
    exhausted: "预算已用尽",
    waiting: "等待完成",
    clear: "检查范围内未发现异常",
    suspected: "疑似异常",
    cancelling: "取消中",
    executing: "执行中",
    verifying: "恢复验证中",
  })[value] ?? value;
export const timeLabel = (value?: string) =>
  value
    ? new Date(value).toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "—";
