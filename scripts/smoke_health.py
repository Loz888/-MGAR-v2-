import subprocess,os,tempfile,pathlib,socket,time,urllib.request,json
subprocess.run(['go','build','-o','bin/orchestratord','./cmd/orchestratord'],check=True)
with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
env=os.environ.copy();env['MGAR_LISTEN']=f'127.0.0.1:{port}'
p=subprocess.Popen(['bin/orchestratord'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
try:
 for attempt in range(100):
  if p.poll() is not None:raise RuntimeError('daemon exited during startup')
  try:
   with urllib.request.urlopen(f'http://127.0.0.1:{port}/healthz',timeout=1) as r:body=json.load(r)
   assert body['status']=='ok' and body['provider']=='mock';break
  except OSError:time.sleep(.05)
 else:raise RuntimeError('HTTP readiness timed out')
 print('HTTP functional response:',body)
finally:
 p.terminate();p.wait(timeout=5)
