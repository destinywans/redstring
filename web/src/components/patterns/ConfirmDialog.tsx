import * as AlertDialog from "@radix-ui/react-alert-dialog";
import { Button } from "../ui/button";
import "./discard-dialog.css";

export default function ConfirmDialog({
  open,
  onOpenChange,
  onConfirm,
  title,
  description,
  confirmLabel,
  cancelLabel = "取消",
  busy = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  title: string;
  description: string;
  confirmLabel: string;
  cancelLabel?: string;
  busy?: boolean;
}) {
  return (
    <AlertDialog.Root
      open={open}
      onOpenChange={(value) => !busy && onOpenChange(value)}
    >
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="ui-discard-overlay" />
        <AlertDialog.Content className="ui-discard-dialog" aria-busy={busy}>
          <AlertDialog.Title>{title}</AlertDialog.Title>
          <AlertDialog.Description>{description}</AlertDialog.Description>
          <div className="ui-discard-actions">
            <AlertDialog.Cancel asChild>
              <Button variant="outline" disabled={busy}>
                {cancelLabel}
              </Button>
            </AlertDialog.Cancel>
            <AlertDialog.Action asChild>
              <Button
                variant="destructive"
                disabled={busy}
                onClick={(event) => {
                  event.preventDefault();
                  onConfirm();
                }}
              >
                {busy ? "处理中…" : confirmLabel}
              </Button>
            </AlertDialog.Action>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}
