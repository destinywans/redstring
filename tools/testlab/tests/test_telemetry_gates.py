import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import telemetry_gates as gates


class TelemetryGateOwnershipTests(unittest.TestCase):
    def test_foreign_component_is_rejected_before_any_query(self):
        with tempfile.TemporaryDirectory() as work, patch.object(gates.components, 'owned', return_value=(Path(work), {'owner': 'testlab-test'}, Path(work))), patch.object(gates.components, 'inspect', return_value={'State': {'Running': True}, 'Config': {'Labels': {gates.components.LABEL: 'foreign'}}}):
            with self.assertRaisesRegex(RuntimeError, 'unowned'):
                gates.targets(Path(work))

    def test_wrong_loopback_port_is_rejected(self):
        info = {'State': {'Running': True}, 'Config': {'Labels': {gates.components.LABEL: 'testlab-test'}}, 'NetworkSettings': {'Ports': {'9090/tcp': [{'HostIp': '0.0.0.0', 'HostPort': '18570'}]}}}
        with tempfile.TemporaryDirectory() as work, patch.object(gates.components, 'owned', return_value=(Path(work), {'owner': 'testlab-test'}, Path(work))), patch.object(gates.components, 'inspect', return_value=info):
            with self.assertRaisesRegex(RuntimeError, 'binding mismatch'):
                gates.targets(Path(work))

    def test_container_run_label_drift_prevents_cleanup(self):
        owner = {'owner': 'testlab-test'}
        def inspect(_):
            if inspect.calls == 0:
                inspect.calls += 1
                return None
            return {'Config': {'Image': 'grafana/grafana:12.1.1', 'Labels': {gates.components.LABEL: owner['owner'], 'aiops.testlab.run': 'foreign'}}, 'Image': 'owned-image'}
        inspect.calls = 0
        with tempfile.TemporaryDirectory() as work, patch.object(gates.components, 'port_free'), patch.object(gates.components, 'inspect', side_effect=inspect), patch.object(gates.components, 'cmd') as command, patch.object(gates.components, 'wait_http', return_value={'database': 'ok', 'version': '12.1.1'}), patch.object(gates, 'emit'):
            with self.assertRaisesRegex(RuntimeError, 'mismatched transient Grafana cleanup'):
                with gates.grafana(owner, Path(work)):
                    pass
            self.assertFalse(any(call.args[0][:3] == ['docker', 'rm', '-f'] for call in command.call_args_list))


if __name__ == '__main__':
    unittest.main()
