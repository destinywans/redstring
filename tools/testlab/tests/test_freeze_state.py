import contextlib
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from runner import Runner, source_fingerprint
from store import Store


class SourceFreezeTests(unittest.TestCase):
    def test_embedded_schema_dictionary_directory_and_hidden_rules(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);package=root/'internal/example';package.mkdir(parents=True)
            source=package/'embed.go';source.write_text('package example\n//go:embed schema.sql pinyin.json templates\nvar data string\n')
            (package/'schema.sql').write_text('CREATE TABLE old(id text);')
            (package/'pinyin.json').write_text('{"旧":"jiu"}')
            templates=package/'templates';templates.mkdir();(templates/'one.txt').write_text('original')
            (templates/'_private.txt').write_text('excluded unless all:')
            paths='internal/example/embed.go\0'
            with patch('runner.subprocess.check_output',return_value=paths.encode()):
                old=source_fingerprint(root)
                for name in ('schema.sql','pinyin.json','templates/one.txt'):
                    path=package/name;original=path.read_text();path.write_text(original+' updated')
                    self.assertNotEqual(old,source_fingerprint(root),name)
                    path.write_text(original)
                (templates/'_private.txt').write_text('changed hidden file')
                self.assertEqual(old,source_fingerprint(root))
                source.write_text('package example\n//go:embed all:templates\nvar data string\n')
                old=source_fingerprint(root);(templates/'_private.txt').write_text('included hidden change')
                self.assertNotEqual(old,source_fingerprint(root))

    def test_embed_glob_and_quoted_space_name_and_ui_is_excluded(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);p=root/'pkg';p.mkdir();(root/'web').mkdir()
            (p/'data.go').write_text('package p\n//go:embed *.sql "space name.json"\nvar data string\n')
            (p/'schema.sql').write_text('v1');(p/'space name.json').write_text('{}');(root/'web/app.go').write_text('UI mock')
            with patch('runner.subprocess.check_output',return_value=b'pkg/data.go\0web/app.go\0'):
                old=source_fingerprint(root);(root/'web/app.go').write_text('UI changed')
                self.assertEqual(old,source_fingerprint(root))
                (p/'space name.json').write_text('{"new":true}');self.assertNotEqual(old,source_fingerprint(root))


class AtomicRunStateTests(unittest.TestCase):
    def test_heartbeat_cannot_overwrite_terminal_run_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            store=Store(tmp);run=store.create_run();store.update('runs',run['id'],status='running')
            original_connect=store.connect;snapshot_read=threading.Event();release=threading.Event();errors=[]
            class Cursor:
                def __init__(self,cursor,sql):self.cursor,self.sql=cursor,sql
                def fetchone(self):
                    row=self.cursor.fetchone()
                    if threading.current_thread().name=='heartbeat-fixture' and self.sql.startswith('SELECT data FROM runs'):
                        snapshot_read.set()
                        if not release.wait(3):raise AssertionError('deterministic heartbeat barrier timed out')
                    return row
                def __getattr__(self,name):return getattr(self.cursor,name)
            class Connection:
                def __init__(self,connection):self.connection=connection
                def execute(self,sql,*args):
                    if threading.current_thread().name=='terminal-fixture' and sql.startswith('BEGIN IMMEDIATE'):
                        release.set()  # Writer serialization must precede the terminal SELECT.
                    return Cursor(self.connection.execute(sql,*args),sql)
                def __getattr__(self,name):return getattr(self.connection,name)
            @contextlib.contextmanager
            def connected():
                with original_connect() as connection:yield Connection(connection)
            def heartbeat():
                try:store.update('runs',run['id'],heartbeat='last-heartbeat')
                except BaseException as error:errors.append(error);release.set()
            def terminal():
                try:store.update('runs',run['id'],status='passed',ended_at='finished')
                except BaseException as error:errors.append(error)
                finally:release.set()
            with patch.object(store,'connect',connected):
                a=threading.Thread(target=heartbeat,name='heartbeat-fixture');a.start()
                self.assertTrue(snapshot_read.wait(3))
                b=threading.Thread(target=terminal,name='terminal-fixture');b.start()
                a.join(4);b.join(4)
                self.assertFalse(a.is_alive());self.assertFalse(b.is_alive());self.assertEqual(errors,[])
            result=store.detail(run['id'])['run']
            self.assertEqual(result['status'],'passed');self.assertEqual(result['ended_at'],'finished')
            self.assertEqual(result['heartbeat'],'last-heartbeat')


class ProtocolFrameTests(unittest.TestCase):
    def run_script(self,script,parser='testlab_json',timeout=5,fingerprint='frozen-source'):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);(root/'tools/testlab').mkdir(parents=True)
            path=root/'fixture.py';path.write_text(script)
            manifest=root/'tools/testlab/suites.json'
            manifest.write_text(json.dumps({'suites':[{'id':'fixture','commands':[{'argv':[sys.executable,str(path)],'parser':parser,'timeout_seconds':timeout}]}]}))
            with patch('runner.source_fingerprint',return_value=fingerprint):return Runner(root).run('fixture')

    def test_valid_large_terminal_frame_is_complete(self):
        detail=self.run_script('import json\nprint(json.dumps({"name":"small","status":"passed"}))\nprint(json.dumps({"name":"large-real-result","status":"passed","actual":"x"*100000}))\n')
        self.assertEqual(detail['run']['status'],'passed')
        self.assertEqual({c['name'] for c in detail['cases']},{'small','large-real-result'})

    def test_malformed_and_oversize_stdout_frame_never_green(self):
        for frame in ('print("{broken JSON")','print(json.dumps({"name":"huge","status":"passed","actual":"x"*(2<<20)}))'):
            with self.subTest(frame=frame[:30]):
                detail=self.run_script('import json\nprint(json.dumps({"name":"small","status":"passed"}))\n'+frame+'\n')
                self.assertEqual(detail['run']['status'],'failed')
                self.assertGreater(detail['summary']['failed'],0)

    def test_stderr_diagnostics_are_not_protocol_results(self):
        detail=self.run_script('import json,sys\nprint("ordinary diagnostic",file=sys.stderr)\nprint(json.dumps({"name":"diagnostic-only","status":"failed"}),file=sys.stderr)\nprint(json.dumps({"name":"real","status":"passed"}))\n')
        self.assertEqual(detail['run']['status'],'passed')
        self.assertEqual([c['name'] for c in detail['cases']],['real'])

    def test_eof_does_not_disable_process_deadline(self):
        start=time.monotonic()
        detail=self.run_script('import os,time\nos.close(1)\nos.close(2)\ntime.sleep(2)\n',parser='command',timeout=.05)
        self.assertLess(time.monotonic()-start,1.2)
        self.assertEqual(detail['run']['status'],'failed')

    def test_unknown_source_fingerprint_cannot_pass(self):
        detail=self.run_script('import json\nprint(json.dumps({"name":"actual","status":"passed"}))\n',fingerprint='unknown')
        self.assertEqual(detail['run']['status'],'blocked')

    def test_final_frame_without_newline_and_invalid_utf8(self):
        detail=self.run_script('import json,sys\nsys.stdout.write(json.dumps({"name":"EOF frame","status":"passed"}))\n')
        self.assertEqual(detail['run']['status'],'passed')
        detail=self.run_script('import json,os\nprint(json.dumps({"name":"good","status":"passed"}),flush=True)\nos.write(1,b\'\\xff\\n\')\n')
        self.assertEqual(detail['run']['status'],'failed')

    def test_reported_interruption_is_not_aggregate_pass(self):
        detail=self.run_script('import json\nprint(json.dumps({"name":"stopped window","status":"interrupted"}))\n')
        self.assertEqual(detail['run']['status'],'blocked')
        self.assertEqual(detail['summary']['interrupted'],1)


if __name__=='__main__':unittest.main()
