import { useState } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { request, post } from "../../lib/api";
import { Button, ErrorNotice, Modal } from "../../components/ui";
import { useWorkspace } from "../../lib/workspace";
import type { Run, Items } from "./types";

export default function RunEvidence({ run }: { run: Run }) {
  const { notify } = useWorkspace();
  const [open, setOpen] = useState(false), [investigation, setInvestigation] = useState(""), [branch, setBranch] = useState("");
  const investigations = useQuery({
    queryKey: ["delivery", "run-investigations", run.application_id, run.environment_id],
    queryFn: () => request<Items<{id:string;title:string;status:string}>>(`/investigations?application_id=${run.application_id}&environment_id=${run.environment_id}`),
    enabled: open,
  });
  const branches = useQuery({
    queryKey: ["delivery", "run-branches", investigation],
    queryFn: () => request<{branches:{id:string;title?:string;name?:string}[]}>(`/investigations/${investigation}`),
    enabled: open && !!investigation,
  });
  const save = useMutation({mutationFn:()=>post(`/delivery/runs/${run.id}/evidence`,{investigation_id:investigation,branch_id:branch}),onSuccess:()=>{setOpen(false);notify("运行快照已加入调查");}});
  return <>
    <Button variant="outline" onClick={()=>{save.reset();setInvestigation("");setBranch("");setOpen(true);}}>加入调查</Button>
    <Modal open={open} onOpenChange={value=>{if(!save.isPending)setOpen(value);}} title="将运行记录加入调查" description="保存当前运行状态、源码版本和来源。目标调查必须属于同一应用与环境。">
      <label>调查<select className="field" value={investigation} onChange={e=>{setInvestigation(e.target.value);setBranch("");}}><option value="">选择调查</option>{investigations.data?.items.filter(i=>i.status!=="closed").map(i=><option key={i.id} value={i.id}>{i.title}</option>)}</select></label>
      <label>节点<select className="field" value={branch} onChange={e=>setBranch(e.target.value)}><option value="">选择节点</option>{branches.data?.branches.map(b=><option key={b.id} value={b.id}>{b.title || b.name || b.id}</option>)}</select></label>
      {investigations.error && <ErrorNotice error={investigations.error}/>}{branches.error && <ErrorNotice error={branches.error}/>}{save.error && <ErrorNotice error={save.error}/>}
      <Button disabled={!branch || save.isPending} onClick={()=>save.mutate()}>{save.isPending?"正在保存…":"保存证据快照"}</Button>
    </Modal>
  </>;
}
