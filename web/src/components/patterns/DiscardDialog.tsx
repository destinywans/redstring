import * as AlertDialog from "@radix-ui/react-alert-dialog";
import { Button } from "../ui/button";
import "./discard-dialog.css";

export default function DiscardDialog({
  open,
  onOpenChange,
  onDiscard,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDiscard: () => void;
}) {
  return (
    <AlertDialog.Root open={open} onOpenChange={onOpenChange}>
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="ui-discard-overlay" />
        <AlertDialog.Content className="ui-discard-dialog">
          <AlertDialog.Title>放弃未保存的修改？</AlertDialog.Title>
          <AlertDialog.Description>
            当前修改还没有保存，关闭后将不会保留。
          </AlertDialog.Description>
          <div className="ui-discard-actions">
            <AlertDialog.Cancel asChild>
              <Button variant="outline">继续编辑</Button>
            </AlertDialog.Cancel>
            <AlertDialog.Action asChild>
              <Button variant="destructive" onClick={onDiscard}>
                放弃修改
              </Button>
            </AlertDialog.Action>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}
