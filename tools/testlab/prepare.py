"""One explicit entrypoint for the owned lightweight environment."""
from pathlib import Path
from store import Store

def prepare_all(root,run_id):
    root=Path(root).resolve()
    import backend,components,runtime,perf
    if root!=backend.ROOT.resolve() or root!=perf.ROOT.resolve():
        raise ValueError('prepare root must match the checkout containing the backend/performance helpers')
    running=[run for run in Store(root).runs() if run['status']=='running']
    if running:raise RuntimeError('tests are running; prepare would restart/reseed their environment')
    from env import prepare
    environment=prepare(root,run_id)
    components.prepare(root)
    backend.setup()  # Install the actual V3 schema before application bindings.
    runtime.configure(root)
    backend.setup()  # Apply the explicit source/trial/webhook configuration.
    perf.prepare('base')
    return environment
