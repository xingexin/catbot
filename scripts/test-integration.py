"""Run integration tests against the dedicated local test database."""
import os
from pathlib import Path
import subprocess

root=Path(__file__).resolve().parent.parent
values={}
for line in (root/".env").read_text().splitlines():
    if "=" in line:
        key,value=line.split("=",1)
        values[key]=value
context=os.environ.get("TEST_DOCKER_CONTEXT","colima-secretary")
docker=["docker","--context",context]
subprocess.run(docker+["exec","secretary-postgres-1","createdb","-U","secretary","secretary_test"],capture_output=True)
env=dict(os.environ)
env["TEST_DATABASE_URL"]="postgres://secretary:"+values["POSTGRES_PASSWORD"]+"@127.0.0.1:5442/secretary_test?sslmode=disable"
env["TEST_TEMPORAL_ADDRESS"]="127.0.0.1:7233"
env["GOCACHE"]=env.get("GOCACHE","/private/tmp/agenttest-go-build")
subprocess.run(["go","test","-race","./..."],cwd=root,env=env,check=True)
