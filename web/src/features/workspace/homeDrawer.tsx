import Pagination, { useListPagination } from "../../components/Pagination";
import { useEffect, useRef, useState } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { Link } from "react-router-dom";
import { ArrowRight, Pin, X } from "../../lib/icons";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import { timeLabel } from "./operations";

export type HomeItem = {
  id: string;
  title: string;
  resource: string;
  progress: string;
  status: string;
  statusLabel: string;
  updatedAt?: string;
  to: string;
  action: string;
  tone?: "warning" | "success";
};

export default function HomeDrawer({
  open,
  title,
  description,
  items,
  pinned,
  onPin,
  onClose,
  children,
}: {
  open: boolean;
  title: string;
  description: string;
  items: HomeItem[];
  pinned: boolean;
  onPin: () => void;
  onClose: () => void;
  children?: React.ReactNode;
}) {
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [order, setOrder] = useState("newest");
  useEffect(() => {
    if (open) {
      setSearch("");
      setStatus("");
      setOrder("newest");
    }
  }, [open, title]);
  const previousFocus = useRef<HTMLElement | null>(null);
  const searchRef = useRef<HTMLDivElement>(null);
  const statuses = Array.from(
    new Map(items.map((item) => [item.status, item.statusLabel])).entries(),
  );
  const visible = items
    .filter(
      (item) =>
        (!status || item.status === status) &&
        `${item.title} ${item.resource} ${item.progress}`
          .toLocaleLowerCase()
          .includes(search.trim().toLocaleLowerCase()),
    )
    .sort(
      (a, b) =>
        (Date.parse(b.updatedAt || "") || 0) -
        (Date.parse(a.updatedAt || "") || 0),
    );
  if (order === "oldest") visible.reverse();
  const pagination = useListPagination(
    visible,
    JSON.stringify([search, status, order, open, title]),
  );
  return (
    <Dialog.Root
      open={open}
      modal={!pinned}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <Dialog.Portal>
        {!pinned && <Dialog.Overlay className="home-drawer-overlay" />}
        <Dialog.Content
          className={`home-drawer${pinned ? " is-pinned" : ""}`}
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            if (
              !(document.activeElement instanceof HTMLElement) ||
              !document.activeElement.closest(".home-drawer")
            )
              previousFocus.current = document.activeElement as HTMLElement;
            searchRef.current
              ?.querySelector<HTMLInputElement>('input[type="search"]')
              ?.focus();
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (previousFocus.current?.isConnected)
              previousFocus.current.focus();
          }}
          onInteractOutside={(event) => {
            if (pinned) event.preventDefault();
          }}
        >
          <header className="home-drawer-heading">
            <Dialog.Title>
              {title}
              <span className="home-count">{items.length}</span>
            </Dialog.Title>
            <button
              className={`btn btn-small${pinned ? " home-pin-active" : ""}`}
              aria-pressed={pinned}
              onClick={onPin}
            >
              <Pin size={17} />
              {pinned ? "取消固定" : "固定"}
            </button>
            <Dialog.Close className="icon-button" aria-label="关闭侧边卡片">
              <X size={22} />
            </Dialog.Close>
          </header>
          <Dialog.Description className="home-drawer-description">
            {description}
          </Dialog.Description>
          <div className="home-drawer-filters" ref={searchRef}>
            <FilterBar label="侧边卡片搜索与筛选">
              <FilterSearch
                value={search}
                onChange={setSearch}
                onSubmit={() => setSearch(search.trim())}
                placeholder="搜索标题、应用或资源"
                label="搜索侧边卡片"
              />
              <FilterSelect
                id="home-drawer-status"
                label="状态"
                options={statuses.map(([value, label]) => ({ value, label }))}
                values={status ? [status] : []}
                onChange={(values) => setStatus(values[0] ?? "")}
              />
              <FilterSelect
                id="home-drawer-sort"
                label="排序"
                options={[{ value: "oldest", label: "最早更新" }]}
                emptyLabel="最近更新"
                values={order === "newest" ? [] : [order]}
                onChange={(values) => setOrder(values[0] ?? "newest")}
              />
              {(search || status || order !== "newest") && (
                <button
                  className="filter-button"
                  onClick={() => {
                    setSearch("");
                    setStatus("");
                    setOrder("newest");
                  }}
                >
                  清除筛选
                </button>
              )}
            </FilterBar>
          </div>
          <div className="home-drawer-scroll">
            {visible.length ? (
              pagination.items.map((item) => (
                <article className="home-drawer-item" key={item.id}>
                  <div className="home-drawer-item-title">
                    <h3>{item.title}</h3>
                    <span className={`home-status ${item.tone || ""}`}>
                      {item.statusLabel}
                    </span>
                  </div>
                  <p>{item.resource}</p>
                  <p className="home-item-progress">{item.progress}</p>
                  <div className="home-drawer-item-footer">
                    <time>{timeLabel(item.updatedAt)}</time>
                    <Link to={item.to} onClick={onClose}>
                      {item.action}
                      <ArrowRight size={17} />
                    </Link>
                  </div>
                </article>
              ))
            ) : (
              <p className="home-empty">
                {items.length ? "没有符合筛选条件的记录" : "当前范围暂无记录"}
              </p>
            )}
            {children}
          </div>
          <Pagination
            page={pagination.page}
            pageSize={pagination.pageSize}
            total={pagination.total}
            onPageChange={pagination.setPage}
            onPageSizeChange={pagination.setPageSize}
            totalLabel={`已加载结果共 ${pagination.total} 条`}
          />
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
