import ConfirmDialog from "./ConfirmDialog";

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
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title="放弃未保存的修改？"
      description="当前修改还没有保存，关闭后将不会保留。"
      cancelLabel="继续编辑"
      confirmLabel="放弃修改"
      onConfirm={onDiscard}
    />
  );
}
