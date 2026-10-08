"""Run integration tests against the dedicated local test database."""
import os
from pathlib import Path
import subprocess
import tempfile
from urllib.parse import quote
from deployment import settings

root=Path(__file__).resolve().parent.parent
env=dict(os.environ)
if env.get("TEST_DOCKER_CONTEXT"):
    env["DOCKER_CONTEXT"]=env["TEST_DOCKER_CONTEXT"]
values=settings(root, env)
compose=[str(root/"scripts"/"compose")]
subprocess.run(compose+["exec","-T","postgres","createdb","-U","secretary","secretary_test"],
               cwd=root, env=env, capture_output=True)
def port(service, number):
    return subprocess.check_output(compose+["port",service,str(number)], cwd=root,
                                   env=env, text=True).strip()
env["TEST_DATABASE_URL"]="postgres://secretary:"+quote(values["POSTGRES_PASSWORD"], safe="")+"@"+port("postgres",5432)+"/secretary_test?sslmode=disable"
env["TEST_TEMPORAL_ADDRESS"]=port("temporal",7233)
env["GOCACHE"]=env.get("GOCACHE",str(Path(tempfile.gettempdir())/"catbot-go-build"))
subprocess.run(["go","test","-race","./..."],cwd=root,env=env,check=True)
