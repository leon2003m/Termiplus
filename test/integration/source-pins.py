#!/usr/bin/env python3
"""Real-kernel regression for workspace source-IP/ARP impersonation.

Run only in a NEW network namespace (requires ip, ebtables and Python stdlib):
    sudo unshare --net python3 test/integration/source-pins.py
Refuses any namespace containing interfaces other than lo. No Docker, external
network, real user, real key or host firewall is used. All namespaces and rules
disappear when the isolated runner exits.
"""
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
text = (ROOT / "deploy/firewall.sh").read_text()
FUNCTIONS = text[text.index("eb_jump_exists() {"):text.index("# sync_ports BRIDGE")]
DISCOVERY = text[text.index("port_macs() {"):text.index("# ebtables -C")]
KEY = "Bearer obviously-fake-victim-key"


def run(*args):
    return subprocess.check_output(args, text=True, timeout=10).strip()


def check(value, description):
    if not value:
        raise AssertionError(description)
    print("PASS " + description, flush=True)


SERVER = '''import http.server,sys
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  with open(sys.argv[2],'a') as f:f.write(self.headers.get('Authorization','')+'\\n')
  self.send_response(200);self.end_headers();self.wfile.write(sys.argv[1].encode())
 def log_message(self,*a):pass
http.server.HTTPServer(('0.0.0.0',8081),Handler).serve_forever()
'''
CLIENT = '''import urllib.request,sys
r=urllib.request.Request('http://'+sys.argv[1]+':8081/',headers={'Authorization':sys.argv[2]})
with urllib.request.urlopen(r,timeout=3) as f:print(f.read().decode())
'''
POISON = '''import socket,struct,sys
mac=lambda s:bytes.fromhex(s.replace(':',''))
a,g=map(mac,sys.argv[1:3]);v=socket.inet_aton(sys.argv[3]);gw=socket.inet_aton(sys.argv[4])
s=socket.socket(socket.AF_PACKET,socket.SOCK_RAW);s.bind(('eth0',0))
for opcode in (1,2):
 packet=g+a+b'\\x08\\x06'+struct.pack('!HHBBH',1,0x0800,6,4,opcode)+a+v+g+gw
 for _ in range(3):s.send(packet)
'''


def main():
    interfaces=json.loads(run('ip','-j','link'))
    if {i['ifname'] for i in interfaces} != {'lo'}:
        sys.exit('Refusing: use a fresh isolated network namespace; this namespace has other interfaces.')
    processes=[]
    with tempfile.TemporaryDirectory(prefix='source-pin-kernel-') as tmp:
        work=Path(tmp)
        shell=work/'functions.sh'
        shell.write_text('set -eu\nBRIDGE=ot-sandbox\nOFFLINE_BRIDGE=ot-offline\n'+DISCOVERY+FUNCTIONS+'\n"$@"\n')
        stubdir=work/'bin';stubdir.mkdir()
        try:
            for network in (0,1):
                bridge='ot-sandbox' if network==0 else 'ot-offline'
                prefix=f'172.30.{network}.'
                run('ip','link','add',bridge,'type','bridge')
                run('ip','link','set',bridge,'up')
                endpoints={};rows=[];docker_rows=[];pid_cases=[]
                for label,last,tail in [('g',2,'02'),('a',150,'0a'),('v',151,'0b')]:
                    name=f'p{network}{label}';peer=f'n{network}{label}';mac=f'02:00:00:00:00:{tail}'
                    process=subprocess.Popen(['unshare','--net','sleep','240']);processes.append(process)
                    # Wait until unshare has entered the child's network namespace.
                    for _ in range(100):
                        if os.readlink(f'/proc/{process.pid}/ns/net') != os.readlink('/proc/self/ns/net'):break
                        time.sleep(.01)
                    else:raise AssertionError('child network namespace startup')
                    run('ip','link','add',name,'type','veth','peer','name',peer)
                    run('ip','link','set',name,'master',bridge);run('ip','link','set',name,'up')
                    run('ip','link','set',peer,'netns',str(process.pid))
                    ns=['nsenter','-t',str(process.pid),'-n']
                    run(*ns,'ip','link','set',peer,'name','eth0')
                    run(*ns,'ip','link','set','eth0','address',mac)
                    run(*ns,'ip','addr','add',prefix+str(last)+'/24','dev','eth0')
                    run(*ns,'ip','link','set','eth0','up');run(*ns,'ip','link','set','lo','up')
                    endpoints[label]=(ns,mac,prefix+str(last));rows.append(f'{name} {mac} {prefix}{last}')
                    docker_rows.append(f'workspace-{label} {mac} {prefix}{last}/24')
                    pid_cases.append(f'workspace-{label}) echo {process.pid} ;;')
                g,gm,gi=endpoints['g'];a,am,ai=endpoints['a'];v,vm,vi=endpoints['v']
                # Only Docker metadata is stubbed. Resolve real host veths and
                # namespace IDs with the actual ip/lsns commands; never enter a
                # workspace namespace to discover its identity.
                docker=stubdir/'docker'
                docker.write_text('#!/bin/sh\ncase "$1" in\nnetwork) printf "%s\\n" '+shlex.quote('\n'.join(docker_rows))+' ;;\ninspect) case "$4" in\n'+'\n'.join(pid_cases)+'\nesac ;;\n*) exit 1 ;;\nesac\n')
                docker.chmod(0o755)
                discovery_env={**os.environ,'PATH':str(stubdir)+':'+os.environ['PATH']}
                discover=lambda:subprocess.check_output(['/bin/sh',str(shell),'port_macs',bridge],text=True,env=discovery_env,timeout=10).splitlines()
                check(sorted(discover())==sorted(rows),bridge+': real host NETNSID resolves every Docker endpoint')
                run(*a,'ip','link','add','forged','type','veth','peer','name','forged-peer')
                run(*a,'ip','link','set','forged','address',vm)
                check(sorted(discover())==sorted(rows),bridge+': forged namespace links cannot alter host port ownership')
                run(*g,'ip','ntable','change','name','arp_cache','dev','eth0','locktime','0')
                for label in ('a','v'):
                    ns=endpoints[label][0]
                    processes.append(subprocess.Popen(ns+['python3','-c',SERVER,label,str(work/(bridge+'-'+label+'.log'))],stdout=subprocess.DEVNULL,stderr=subprocess.PIPE))
                time.sleep(.15)
                client=lambda:run(*g,'python3','-c',CLIENT,vi,KEY)
                poison=lambda:run(*a,'python3','-c',POISON,am,gm,vi,gi)
                victim_neighbour=lambda:run(*g,'ip','neigh','replace',vi,'lladdr',vm,'nud','reachable','dev','eth0')
                victim_neighbour();check(client()=='v',bridge+': ordinary victim request works before guard')
                run(*a,'ip','addr','add',vi+'/32','dev','eth0')
                poison();time.sleep(.05)
                check(client()=='a',bridge+': reproduced own-MAC victim-IP impersonation before guard')
                logfile=work/(bridge+'-a.log')
                check(KEY in logfile.read_text(),bridge+': only dummy victim bearer intercepted in reproduction')
                before=logfile.read_text()
                run('/bin/sh',str(shell),'sync_source_pins',bridge,'\n'.join(rows))
                victim_neighbour()
                for _ in range(3):poison()
                check(vm in run(*g,'ip','neigh','show',vi),bridge+': malicious ARP request/reply cannot replace victim neighbour')
                check(client()=='v',bridge+': guarded gateway request reaches actual victim')
                check(logfile.read_text()==before,bridge+': attacker receives no victim bearer after fix')
                # Explicitly ignore bridge port locks: this proves the new guard
                # independently rejects both forged IPs and forged Ethernet MACs.
                run(*a,'ip','link','set','eth0','address',vm)
                run(*a,'python3','-c',POISON,vm,gm,vi,gi)
                check(vm in run(*g,'ip','neigh','show',vi) and client()=='v',bridge+': forging victim MAC as well cannot steal its traffic')
                run(*a,'ip','link','set','eth0','address',am)
                run('/bin/sh',str(shell),'source_pins_current',bridge,'\n'.join(rows))
                before_rules=run('ebtables','-t','nat','-L','PREROUTING')
                run('/bin/sh',str(shell),'sync_source_pins',bridge,'\n'.join(rows))
                check(run('ebtables','-t','nat','-L','PREROUTING')==before_rules,bridge+': refresh is idempotent without duplicate jumps')
                # An unresolved port with a live interface/FDB must also lose its
                # allowance; no reliance on locked-port or neighbour-table state.
                run('/bin/sh',str(shell),'sync_source_pins',bridge,'\n'.join(r for r in rows if not r.startswith(f'p{network}a ')))
                poison();check(client()=='v',bridge+': unresolved attacker remains blocked')
        finally:
            for p in reversed(processes):
                p.terminate()
            for p in processes:
                try:p.wait(timeout=3)
                except subprocess.TimeoutExpired:p.kill();p.wait()
    print('PASS isolated kernel ARP/HTTP regressions on both bridges',flush=True)


if __name__=='__main__':
    main()
