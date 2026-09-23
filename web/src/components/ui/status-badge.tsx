import {
  AlertTriangle,
  CheckCircle2,
  CircleDashed,
  CircleHelp,
  FlaskConical,
  type IconComponent,
} from "../../lib/icons";
import { cn } from "../../lib/utils";
import "./system.css";

/**
 * How far a value can be trusted. Sample data must never pass as live state,
 * so every surface showing mixed sources labels them with this badge.
 */
export type DataTrust = "live" | "sample" | "unverified" | "unknown" | "error";

const trustStyles: Record<
  DataTrust,
  { label: string; hint: string; icon: IconComponent }
> = {
  live: {
    label: "真实数据",
    hint: "来自已接入系统的实时或最近一次采集结果",
    icon: CheckCircle2,
  },
  sample: {
    label: "示例数据",
    hint: "用于界面预览，不代表真实运行状态",
    icon: FlaskConical,
  },
  unverified: {
    label: "已配置 · 未验证",
    hint: "配置已保存，但尚未确认能连通",
    icon: CircleDashed,
  },
  unknown: {
    label: "状态未知",
    hint: "暂时无法判断这项数据的来源或新鲜度",
    icon: CircleHelp,
  },
  error: {
    label: "读取异常",
    hint: "最近一次读取失败，展示的内容可能已过期",
    icon: AlertTriangle,
  },
};

export function StatusBadge({
  trust,
  label,
  hint,
  className,
}: {
  trust: DataTrust;
  label?: string;
  hint?: string;
  className?: string;
}) {
  const style = trustStyles[trust];
  const Icon = style.icon;
  return (
    <span
      className={cn("ui-status-badge", `is-${trust}`, className)}
      title={hint ?? style.hint}
    >
      <Icon size={12} weight="bold" aria-hidden="true" />
      {label ?? style.label}
    </span>
  );
}
