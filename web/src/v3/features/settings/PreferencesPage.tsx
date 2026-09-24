import type { ReactNode } from "react";
import { Page } from "../../app/Shell";
import { usePreferences, type Density, type Locale, type ThemePreference } from "../../app/preferences";
import { Badge, Card, Col, Icon, Muted, Row, Strong, Toggle, cx, type IconName } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { api } from "../../api/client";
import "./preferences.css";

function Option({ on, onClick, icon, title, sub, preview }: { on: boolean; onClick: () => void; icon?: IconName; title: ReactNode; sub?: ReactNode; preview?: ReactNode }) {
  return (
    <button type="button" role="radio" aria-checked={on} className={cx("pref-option lift", on && "on")} onClick={onClick}>
      {preview}
      <span className="v3-row" style={{ gap: 8 }}>
        {icon && <Icon name={icon} size={15} />}
        <Strong>{title}</Strong>
        <span style={{ flexGrow: 1 }} />
        <span className={cx("radio", on && "on")} />
      </span>
      {sub && <Muted size={11.5}>{sub}</Muted>}
    </button>
  );
}

function ThemePreview({ dark }: { dark: boolean | "split" }) {
  const pane = (d: boolean) => (
    <span className="tp-pane" data-v3-theme={d ? "dark" : "light"}>
      <span className="tp-side" />
      <span className="tp-main">
        <span className="tp-bar" />
        <span className="tp-card" />
        <span className="tp-card short" />
      </span>
    </span>
  );
  return <span className="theme-preview">{dark === "split" ? <>{pane(false)}{pane(true)}</> : pane(dark)}</span>;
}

function Row2({ title, desc, children }: { title: ReactNode; desc: ReactNode; children: ReactNode }) {
  return (
    <div className="pref-row">
      <Col gap={2} style={{ width: 240, flex: "none" }}>
        <Strong size={13}>{title}</Strong>
        <Muted size={12}>{desc}</Muted>
      </Col>
      <div className="grow">{children}</div>
    </div>
  );
}

export default function PreferencesPage() {
  const { tx } = useI18n();
  const p = usePreferences();
  const sync = (patch: Record<string, unknown>) => void api("PATCH", "/me/preferences", { body: patch });
  const setTheme = (t: ThemePreference) => {
    p.setTheme(t);
    sync({ theme: t });
  };
  const setLocale = (l: Locale) => {
    p.setLocale(l);
    sync({ locale: l });
    toast(l === "zh-CN" ? "界面语言已切换为中文" : "Interface language set to English");
  };
  const setDensity = (d: Density) => {
    p.setDensity(d);
    sync({ density: d });
  };
  return (
    <Page crumbs={[tx("设置", "Settings"), tx("偏好设置", "Preferences")]}>
      <div className="v3-content" style={{ maxWidth: 980 }}>
        <div className="page-h1">
          <Col gap={4}>
            <h1>{tx("偏好设置", "Preferences")}</h1>
            <Muted size={12.5}>{tx("只影响你自己的界面，保存在你的账号中，在其他设备上同样生效。", "Only affects your own interface. Saved to your account and applied on every device.")}</Muted>
          </Col>
        </div>
        <Card className="anim-rise">
          <Row2 title={tx("主题", "Theme")} desc={tx("选择浅色、深色，或跟随操作系统自动切换。", "Light, dark, or follow your operating system.")}>
            <div className="pref-options" role="radiogroup" aria-label={tx("主题", "Theme")}>
              <Option on={p.theme === "light"} onClick={() => setTheme("light")} icon="sun" title={tx("浅色", "Light")} preview={<ThemePreview dark={false} />} />
              <Option on={p.theme === "dark"} onClick={() => setTheme("dark")} icon="moon" title={tx("深色", "Dark")} preview={<ThemePreview dark />} />
              <Option on={p.theme === "system"} onClick={() => setTheme("system")} icon="monitor" title={tx("跟随系统", "System")} preview={<ThemePreview dark="split" />} />
            </div>
          </Row2>
          <Row2 title={tx("语言", "Language")} desc={tx("界面文字、日期与数字格式，以及 AI 回答默认使用的语言。", "Interface text, date and number formats, and the default language of AI answers.")}>
            <div className="pref-options" role="radiogroup" aria-label={tx("语言", "Language")}>
              <Option on={p.locale === "zh-CN"} onClick={() => setLocale("zh-CN")} icon="globe" title="简体中文" sub="2026年9月24日 · 1,234.5" />
              <Option on={p.locale === "en-US"} onClick={() => setLocale("en-US")} icon="globe" title="English" sub="Sep 24, 2026 · 1,234.5" />
            </div>
          </Row2>
          <Row2 title={tx("信息密度", "Density")} desc={tx("紧凑模式缩小表格行高与页面间距。", "Compact reduces table row height and page spacing.")}>
            <div className="pref-options" role="radiogroup" aria-label={tx("信息密度", "Density")}>
              <Option on={p.density === "comfortable"} onClick={() => setDensity("comfortable")} icon="layers" title={tx("舒适", "Comfortable")} />
              <Option on={p.density === "compact"} onClick={() => setDensity("compact")} icon="grid" title={tx("紧凑", "Compact")} />
            </div>
          </Row2>
          <Row2 title={tx("减少动效", "Reduce motion")} desc={tx("关闭抽屉滑入、图表绘入等动画。系统开启“减少动态效果”时会自动生效。", "Turns off drawer slides, chart draw-ins and similar animation. Applied automatically when your OS asks for reduced motion.")}>
            <Row gap={10}>
              <Toggle on={p.reduceMotion} label={tx("减少动效", "Reduce motion")} onChange={(v) => { p.setReduceMotion(v); sync({ reduce_motion: v }); }} />
              <Muted>{p.reduceMotion ? tx("已开启", "On") : tx("已关闭", "Off")}</Muted>
            </Row>
          </Row2>
        </Card>
        <Row gap={8}>
          <Icon name="info" size={14} color="var(--muted)" />
          <Muted>{tx("快捷切换：⌘K 输入“主题”或“语言”，或点击左下角头像。", "Shortcut: type “theme” or “language” in ⌘K, or use the avatar menu at the bottom left.")}</Muted>
          <Badge tone="accent">⌘K</Badge>
        </Row>
      </div>
    </Page>
  );
}
