import { useState } from "react";
import { Button } from "../../components/ui/button";
import { Input, Textarea } from "../../components/ui/input";
import { Select } from "../../components/ui/select";
import { Sheet } from "../../components/ui/sheet";
import {
  FilterGroup,
  FilterPicker,
} from "../../components/patterns/FilterPicker";
import { Search, SlidersHorizontal } from "../../lib/icons";
import "./component-sample.css";

const departments = [
  { value: "platform", label: "平台工程" },
  { value: "commerce", label: "交易研发" },
  { value: "data", label: "数据平台" },
  { value: "global", label: "国际化基础设施与跨区域可靠性工程团队" },
  { value: "archived", label: "历史业务团队（停用）", disabled: true },
];
const tags = [
  "核心链路",
  "有值班团队",
  "已接入指标",
  "已接入日志",
  "容器应用",
  "虚拟机应用",
  "内部服务",
  "对外服务",
  "有 Runbook",
  "试运行",
  "灰度环境",
  "待完善资料",
].map((label) => ({ value: label, label }));
const samples = [
  {
    name: "订单服务",
    key: "order-api",
    department: "commerce",
    tags: ["核心链路", "容器应用", "已接入指标"],
  },
  {
    name: "告警接入服务",
    key: "alert-ingest",
    department: "platform",
    tags: ["有值班团队", "容器应用", "已接入日志"],
  },
  {
    name: "指标查询服务",
    key: "metrics-query",
    department: "data",
    tags: ["内部服务", "有 Runbook", "已接入指标"],
  },
];

export default function ComponentSamplePage() {
  const [query, setQuery] = useState("");
  const [department, setDepartment] = useState<string[]>([]);
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [formDepartment, setFormDepartment] = useState("");
  const [notice, setNotice] = useState("");
  const [sheet, setSheet] = useState<"form" | "detail" | null>(null);
  const visible = samples.filter(
    (sample) =>
      `${sample.name} ${sample.key}`
        .toLowerCase()
        .includes(query.trim().toLowerCase()) &&
      (!department.length || department.includes(sample.department)) &&
      (!selectedTags.length ||
        selectedTags.some((tag) => sample.tags.includes(tag))),
  );
  function resetFilters() {
    setQuery("");
    setDepartment([]);
    setSelectedTags([]);
  }

  return (
    <section className="component-sample-page">
      <header className="component-sample-heading">
        <div>
          <span className="component-sample-eyebrow">界面样板</span>
          <h1>组件与交互</h1>
          <p>
            验证紧凑控件、筛选状态与覆盖式卡片。所有内容均为样例数据，填写与选择不会保存。
          </p>
        </div>
        <span className="component-sample-demo-label">仅本页预览</span>
      </header>

      <section
        className="component-sample-card"
        aria-labelledby="component-sample-filter-heading"
      >
        <div className="component-sample-section-heading">
          <div>
            <h2 id="component-sample-filter-heading">搜索与筛选</h2>
            <p>多选后点击应用；取消或点击外部会放弃当前草稿。</p>
          </div>
          <Button variant="ghost" size="sm" onClick={resetFilters}>
            重置筛选
          </Button>
        </div>
        <div className="component-sample-toolbar">
          <div className="component-sample-search">
            <Search size={16} aria-hidden="true" />
            <Input
              aria-label="搜索样例应用"
              placeholder="搜索应用名称或标识"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <FilterGroup>
            <FilterPicker
              id="component-sample-department"
              label="所属部门"
              options={departments}
              value={department}
              onChange={setDepartment}
              searchable
              width={300}
            />
            <FilterPicker
              id="component-sample-tags"
              label="应用标签"
              options={tags}
              value={selectedTags}
              onChange={setSelectedTags}
              multiple
              searchable
            />
          </FilterGroup>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setQuery("不存在的样例")}
          >
            展示无结果
          </Button>
        </div>
        <div className="component-sample-results" aria-live="polite">
          {visible.length ? (
            visible.map((sample) => (
              <article className="component-sample-result" key={sample.key}>
                <div>
                  <h3>{sample.name}</h3>
                  <span className="component-sample-meta">{sample.key}</span>
                </div>
                <span className="component-sample-result-department">
                  {
                    departments.find((item) => item.value === sample.department)
                      ?.label
                  }
                </span>
                <div className="component-sample-tags">
                  {sample.tags.map((tag) => (
                    <span key={tag}>{tag}</span>
                  ))}
                </div>
              </article>
            ))
          ) : (
            <div className="component-sample-empty">
              <SlidersHorizontal size={24} aria-hidden="true" />
              <h3>没有匹配的样例应用</h3>
              <p>试试其他关键词，或清空当前筛选。</p>
              <Button variant="outline" size="sm" onClick={resetFilters}>
                清空筛选
              </Button>
            </div>
          )}
        </div>
        <p className="component-sample-result-count">
          当前显示 {visible.length} / {samples.length} 个样例应用
        </p>
      </section>

      <div className="component-sample-columns">
        <section
          className="component-sample-card"
          aria-labelledby="component-sample-form-heading"
        >
          <div className="component-sample-section-heading">
            <div>
              <h2 id="component-sample-form-heading">输入与选择</h2>
              <p>支持键盘操作、清空选择和原生表单取值。</p>
            </div>
          </div>
          <form
            className="component-sample-form"
            onSubmit={(event) => {
              event.preventDefault();
              const data = new FormData(event.currentTarget);
              setNotice(
                `已预览“${String(data.get("applicationName") || "未命名应用")}”，所属部门：${departments.find((item) => item.value === data.get("department"))?.label ?? "未选择"}。样例未保存。`,
              );
            }}
          >
            <label
              className="component-sample-field"
              htmlFor="component-sample-name"
            >
              <span>应用名称</span>
              <Input
                id="component-sample-name"
                name="applicationName"
                placeholder="例如：订单服务"
                required
                maxLength={80}
              />
              <small>必填，用于体验表单校验。</small>
            </label>
            <label
              className="component-sample-field"
              htmlFor="component-sample-form-department"
            >
              <span>所属部门</span>
              <Select
                id="component-sample-form-department"
                aria-label="表单所属部门"
                name="department"
                options={departments}
                value={formDepartment}
                onValueChange={setFormDepartment}
                placeholder="选择所属部门（可清空）"
              />
            </label>
            <label
              className="component-sample-field"
              htmlFor="component-sample-disabled"
            >
              <span>系统标识</span>
              <Input
                id="component-sample-disabled"
                value="sample-app-readonly"
                disabled
              />
              <small>禁用状态样板。</small>
            </label>
            <label
              className="component-sample-field"
              htmlFor="component-sample-description"
            >
              <span>应用描述</span>
              <Textarea
                id="component-sample-description"
                name="description"
                placeholder="描述应用用途与维护范围"
                rows={3}
              />
            </label>
            <div className="component-sample-form-actions">
              <Button type="submit">预览表单值</Button>
              <Button variant="outline" disabled>
                暂不可用
              </Button>
            </div>
            {notice && (
              <p role="status" className="component-sample-notice">
                {notice}
              </p>
            )}
          </form>
        </section>

        <section
          className="component-sample-card"
          aria-labelledby="component-sample-sheet-heading"
        >
          <div className="component-sample-section-heading">
            <div>
              <h2 id="component-sample-sheet-heading">覆盖式卡片</h2>
              <p>页面布局保持不变，卡片内容独立滚动。</p>
            </div>
          </div>
          <div className="component-sample-sheet-demo">
            <div className="component-sample-sheet-preview" aria-hidden="true">
              <div />
              <div />
              <div />
              <span />
            </div>
            <div className="component-sample-sheet-actions">
              <Button onClick={() => setSheet("form")}>打开表单卡片</Button>
              <Button variant="outline" onClick={() => setSheet("detail")}>
                打开宽详情卡片
              </Button>
            </div>
          </div>
          <ul className="component-sample-hints">
            <li>表单卡片宽 600px，详情卡片宽 1080px。</li>
            <li>窄屏随视口收缩，标题和操作区始终保留。</li>
            <li>支持 Esc、点击遮罩和关闭按钮。</li>
            <li>关闭后焦点回到打开卡片的控件。</li>
          </ul>
        </section>
      </div>

      <Sheet
        open={sheet !== null}
        onOpenChange={(open) => {
          if (!open) setSheet(null);
        }}
        title={
          sheet === "detail" ? "样例应用 · 详情卡片" : "样例应用 · 表单卡片"
        }
        description="这是交互样板，不连接真实应用，也不会保存任何填写内容。"
        size={sheet ?? "form"}
        footer={
          <>
            <span className="component-sample-sheet-footer-note">
              样例内容 · 不会保存
            </span>
            <Button variant="outline" onClick={() => setSheet(null)}>
              关闭预览
            </Button>
          </>
        }
      >
        <div className="component-sample-sheet-content">
          <label
            className="component-sample-field"
            htmlFor="component-sample-sheet-name"
          >
            <span>应用名称</span>
            <Input
              id="component-sample-sheet-name"
              defaultValue="订单服务（样例）"
            />
          </label>
          <label
            className="component-sample-field"
            htmlFor="component-sample-sheet-team"
          >
            <span>所属部门</span>
            <Select
              id="component-sample-sheet-team"
              aria-label="卡片所属部门"
              options={departments}
              defaultValue="commerce"
            />
          </label>
          {Array.from({ length: 10 }, (_, index) => (
            <section className="component-sample-long-section" key={index}>
              <span className="component-sample-meta">
                样例分区 {String(index + 1).padStart(2, "0")}
              </span>
              <h3>
                {
                  [
                    "应用说明",
                    "维护范围",
                    "关联服务",
                    "只读数据入口",
                    "值班与交接",
                  ][index % 5]
                }
              </h3>
              <p>
                此处用于验证长内容的滚动、段落阅读和卡片边界。滚动时标题与底部操作保持可见；这些文字不代表任何真实业务状态。
              </p>
            </section>
          ))}
        </div>
      </Sheet>
    </section>
  );
}
