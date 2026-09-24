import { useLocation } from "react-router-dom";
import { Page } from "../app/Shell";
import { Btn, Empty } from "../ui/core";
import { useI18n } from "../i18n";

/** Fallback for routes that are not part of the v3 navigation. */
export default function Pending() {
  const { tx } = useI18n();
  const { pathname } = useLocation();
  return (
    <Page crumbs={[pathname]}>
      <Empty icon="grid" title={tx("页面不存在", "Page not found")} desc={pathname} action={<Btn to="/" icon="home">{tx("回到总览", "Back to overview")}</Btn>} />
    </Page>
  );
}
