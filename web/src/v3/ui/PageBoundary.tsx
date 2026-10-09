import { Component, type ReactNode } from "react";
import { Btn, Empty } from "./core";
import { useI18n } from "../i18n";

type Props = { children: ReactNode; resetKey: string };
type State = { error: Error | null };

/**
 * Keeps one broken page from unmounting the whole workspace: the sidebar, command palette
 * and Ask AI stay usable, and navigating elsewhere (a new resetKey) clears the error.
 */
export class PageBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidUpdate(prev: Props) {
    if (prev.resetKey !== this.props.resetKey && this.state.error) this.setState({ error: null });
  }

  render() {
    if (!this.state.error) return this.props.children;
    return <PageFailed error={this.state.error} retry={() => this.setState({ error: null })} />;
  }
}

function PageFailed({ error, retry }: { error: Error; retry: () => void }) {
  const { tx } = useI18n();
  return (
    <main className="v3-page">
      <div className="v3-content" role="alert">
        <Empty
          icon="alert"
          title={tx("这个页面没能显示出来", "This page couldn't be displayed")}
          desc={
            <>
              {tx("其他页面不受影响。可以重试，或从左侧进入其他页面。", "Other pages still work. Retry, or open another page from the sidebar.")}
              <br />
              <code style={{ fontSize: "var(--fs-sub)" }}>{error.message}</code>
            </>
          }
          action={
            <Btn h={30} icon="rotate" onClick={retry}>
              {tx("重试", "Retry")}
            </Btn>
          }
        />
      </div>
    </main>
  );
}
