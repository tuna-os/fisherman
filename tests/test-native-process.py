import importlib.util,sys,tempfile,time,unittest,runpy,shutil
from pathlib import Path
spec=importlib.util.spec_from_file_location('owned',Path(__file__).with_name('native-process.py'));M=importlib.util.module_from_spec(spec);spec.loader.exec_module(M)
class Controls(unittest.TestCase):
 def setUp(self):M.RECEIPT.clear()
 def test_nonzero_numeric_status_and_reap(self):
  with self.assertRaises(M.CommandFailure):M.run([sys.executable,'-c','import sys;print("actual output");sys.exit(7)'],'nonzero')
  r=M.RECEIPT['commands'][-1];self.assertEqual(r['returnCode'],7);self.assertTrue(r['reap']['groupEmptyObserved']);self.assertIn('actual output',r['stdout']['preview'])
 def test_timeout_reaps_owned_descendant_before_return(self):
  with tempfile.TemporaryDirectory(prefix='.native-process-',dir=Path(__file__).parents[1]) as temporary:
   marker=Path(temporary)/'child'
   script='import os,time,pathlib;pid=os.fork();pathlib.Path('+repr(str(marker))+').write_text(str(os.getpid()));time.sleep(30)'
   start=time.monotonic()
   with self.assertRaises(TimeoutError):M.run([sys.executable,'-c',script],'timeout',timeout=.5)
   self.assertLess(time.monotonic()-start,6);self.assertTrue(marker.exists(),'owned code must execute')
   r=M.RECEIPT['commands'][-1];self.assertTrue(r['reap']['parentWaitObserved']);self.assertTrue(r['reap']['groupEmptyObserved']);self.assertFalse(M.rows(r['pid']))
 def test_output_limit_reaps_group(self):
  with self.assertRaises(M.CommandFailure):M.run([sys.executable,'-c','import os;os.write(1,b"x"*1100000)'],'output-limit')
  r=M.RECEIPT['commands'][-1];self.assertTrue(r['reap']['groupEmptyObserved']);self.assertGreater(r['stdout']['bytes'],1048576)
 def test_invalid_utf8_failure_retains_numeric_status(self):
  with self.assertRaises(M.CommandFailure):M.run([sys.executable,'-c','import os;os.write(2,bytes([255])*4096);os._exit(9)'],'binary-failure')
  r=M.RECEIPT['commands'][-1];self.assertEqual(r['returnCode'],9);self.assertEqual(r['stderr']['bytes'],4096);self.assertTrue(r['reap']['groupEmptyObserved'])
 def test_unobserved_reap_preserves_scratch(self):
  probe=runpy.run_path(str(Path(__file__).with_name('native-local-id.py')))
  receipt=probe['RECEIPT'];receipt.clear();receipt['commands']=[{'processStarted':True}]
  with probe['scratch']('.preserve-control-',dir=Path(__file__).parents[1]) as path:
   Path(path,'proof').write_text('owned test')
  self.assertTrue(Path(path,'proof').exists());self.assertEqual(receipt['preservedScratch'],[path])
  shutil.rmtree(path)
 def test_missing_executable_records_no_process(self):
  with self.assertRaises(FileNotFoundError):M.run(['/does-not-exist/native-owned'],'missing')
  r=M.RECEIPT['commands'][-1];self.assertFalse(r['processStarted']);self.assertIsNone(r['returnCode'])
if __name__=='__main__':unittest.main()
