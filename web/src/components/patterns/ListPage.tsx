import type { ReactNode } from "react";
import { cn } from "../../lib/utils";
import "./list-page.css";

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="ui-list-page-header">
      <div>
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      {actions && <div className="ui-list-page-actions">{actions}</div>}
    </header>
  );
}

export function ListPage({
  title,
  description,
  actions,
  className,
  children,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <section className={cn("ui-list-page", className)}>
      <PageHeader title={title} description={description} actions={actions} />
      {children}
    </section>
  );
}

export function ListPanel({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return <div className={cn("ui-list-panel", className)}>{children}</div>;
}
