import { useLocation, useNavigate } from "react-router-dom";
import { create } from "zustand";
import { AiAvatar, Muted, Strong } from "../ui/core";
import { Drawer } from "../ui/overlays";
import { Composer, NextSteps, type SendExtra } from "../ui/Composer";
import { newConversationUrl } from "../api/stream";
import { useI18n } from "../i18n";
import { locate } from "./nav";

export const useAskAi = create<{ isOpen: boolean; draft: string; setOpen: (v: boolean) => void; open: (draft?: string) => void }>((set) => ({
  isOpen: false,
  draft: "",
  setOpen: (isOpen) => set({ isOpen }),
  open: (draft = "") => set({ isOpen: true, draft }),
}));

/** "Ask AI" side drawer available on every page; it carries the current view as context. */
export function AskAiDrawer() {
  const { isOpen, setOpen, draft } = useAskAi();
  const { tx, tl } = useI18n();
  const nav = useNavigate();
  const { pathname } = useLocation();
  const where = locate(pathname);
  const view = where.child ? tl(where.child.label) : where.section ? tl(where.section.label) : tx("当前页面", "This page");
  const start = (text: string, x: SendExtra = { files: [], choice: null }) => {
    setOpen(false);
    nav(`${newConversationUrl(text, [], x)}&context=${encodeURIComponent(pathname)}`);
  };
  return (
    <Drawer open={isOpen} onClose={() => setOpen(false)} width={420} title={tx("问 AI", "Ask AI")} sub={tx("会带上当前视图：{v}", "Includes the current view: {v}", { v: view })} icon={<AiAvatar size={28} />}>
      <div className="v3-col" style={{ gap: 16, padding: 16, height: "100%" }}>
        <div className="v3-col" style={{ gap: 8 }}>
          <Strong>{tx("试试这样问", "Try asking")}</Strong>
          <NextSteps
            items={[
              { icon: "activity", label: tx("解释这页的异常", "Explain what looks wrong here"), onClick: () => start(tx("解释这页的异常", "Explain what looks wrong here")) },
              { icon: "file", label: tx("总结最近 1 小时", "Summarize the last hour"), onClick: () => start(tx("总结最近 1 小时", "Summarize the last hour")) },
              { icon: "branch", label: tx("发起调查", "Start an investigation"), onClick: () => start(tx("基于当前视图发起调查", "Investigate based on this view")) },
            ]}
          />
        </div>
        <Muted size={11.5}>{tx("AI 只读取证；任何写操作都会生成提案，经人工批准后执行。", "The AI only reads. Any write becomes a proposal that a person approves.")}</Muted>
        <span style={{ flexGrow: 1 }} />
        <Composer key={draft} placeholder={draft || tx("问一个问题…", "Ask a question…")} autoFocus attachments={[{ kind: "view", id: "view", label: view }]} onSend={(t, _att, x) => start(t || draft, x)} />
      </div>
    </Drawer>
  );
}
