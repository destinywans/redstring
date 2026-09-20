import {
  ResponsiveContainer,
  AreaChart,
  Area,
  CartesianGrid,
  XAxis,
  YAxis,
  Tooltip,
  ReferenceLine,
  ReferenceArea,
} from "recharts";
export type MetricPoint = { time: string; value: number };
export function MetricChart({
  points,
  color = "var(--accent)",
  unit = "%",
  height = 205,
  marker = "14:03",
  recovery = false,
}: {
  points: MetricPoint[];
  color?: string;
  unit?: string;
  height?: number;
  marker?: string;
  recovery?: boolean;
}) {
  const gradient = `metric-${color.replace(/[^a-z]/gi, "")}-${unit.replace(/[^a-z]/gi, "")}`;
  return (
    <div
      className="metric-chart"
      role="img"
      aria-label={`指标趋势，共 ${points.length} 个采样点`}
      style={{ height }}
    >
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart
          data={points}
          margin={{ top: 20, right: 10, bottom: 0, left: 0 }}
        >
          <defs>
            <linearGradient id={gradient} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity={0.16} />
              <stop offset="100%" stopColor={color} stopOpacity={0.015} />
            </linearGradient>
          </defs>
          <CartesianGrid
            stroke="var(--border)"
            strokeDasharray="3 3"
            vertical={false}
          />
          <XAxis
            dataKey="time"
            tick={{ fill: "var(--muted)", fontSize: 11 }}
            axisLine={false}
            tickLine={false}
            minTickGap={35}
          />
          <YAxis
            width={40}
            tick={{ fill: "var(--muted)", fontSize: 11 }}
            axisLine={false}
            tickLine={false}
            tickFormatter={(v) => `${v}${unit}`}
            domain={[0, "auto"]}
          />
          <Tooltip
            contentStyle={{
              background: "var(--panel)",
              border: "1px solid var(--border)",
              borderRadius: 8,
            }}
          />
          <ReferenceLine
            x={marker}
            stroke="var(--accent)"
            strokeDasharray="4 3"
            label={{
              value: recovery ? "恢复配置" : "发布 v2.8.1",
              fill: "var(--accent)",
              fontSize: 11,
              position: "insideTopLeft",
            }}
          />
          {recovery && (
            <ReferenceArea
              x1="14:20"
              x2="14:23"
              fill="var(--success)"
              fillOpacity={0.08}
            />
          )}
          <Area
            type="monotone"
            dataKey="value"
            name="指标"
            stroke={color}
            strokeWidth={2}
            fill={`url(#${gradient})`}
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
export const incidentPoints: MetricPoint[] = Array.from(
  { length: 31 },
  (_, i) => ({
    time: `${13 + Math.floor((30 + i * 2) / 60)}:${String((30 + i * 2) % 60).padStart(2, "0")}`,
    value:
      i < 16
        ? 0.2 + (i % 3) * 0.04
        : Math.round(
            (8.2 - 7 * Math.exp(-(i - 16) / 1.6) + (i % 3) * 0.08) * 100,
          ) / 100,
  }),
);
export const recoveryPoints: MetricPoint[] = Array.from(
  { length: 31 },
  (_, i) => ({
    time: `14:${String(i).padStart(2, "0")}`,
    value:
      i < 8
        ? 0.2
        : i < 16
          ? Math.min(8.4, (i - 7) * 1.15)
          : i < 19
            ? 8.4 - (i - 16) * 0.23
            : Math.max(0.3, 7.5 * Math.exp(-(i - 19) / 1.1)),
  }),
);
