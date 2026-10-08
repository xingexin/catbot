import os
import importlib.util
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from deployment import ROOT, colima_profile, settings


class DependencySettingsTest(unittest.TestCase):
    def test_upgrade_keeps_local_dependency_enabled(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / ".env").write_text("ADMIN_PASSWORD=fixture\n")
            result = settings(root, {})
            self.assertEqual(result["NAPCAT_ENABLED"], "true")
            self.assertEqual(result["ONEBOT_URL"], "http://napcat:3000")
            self.assertEqual(result["COMPOSE_PROJECT_NAME"], "secretary")

    def test_new_installation_uses_catbot_project(self):
        with tempfile.TemporaryDirectory() as path:
            self.assertEqual(settings(Path(path), {})["COMPOSE_PROJECT_NAME"], "catbot")

    def test_explicit_project_is_preserved_and_environment_can_override(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / ".env").write_text("COMPOSE_PROJECT_NAME=my-catbot\n")
            self.assertEqual(settings(root, {})["COMPOSE_PROJECT_NAME"], "my-catbot")
            self.assertEqual(settings(root, {"COMPOSE_PROJECT_NAME": "test-catbot"})[
                "COMPOSE_PROJECT_NAME"], "test-catbot")

    def test_invalid_project_fails_before_selecting_volumes(self):
        with tempfile.TemporaryDirectory() as path:
            with self.assertRaises(ValueError):
                settings(Path(path), {"COMPOSE_PROJECT_NAME": "Bad Project"})

    def test_disabled_has_no_implicit_local_endpoint(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / ".env").write_text("NAPCAT_ENABLED=false\nONEBOT_URL=\n")
            self.assertEqual(settings(root, {})["ONEBOT_URL"], "")

    def test_external_endpoint_and_environment_override(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / ".env").write_text('NAPCAT_ENABLED=true\nONEBOT_URL="http://external:9000"\n')
            result = settings(root, {"NAPCAT_ENABLED": "false"})
            self.assertEqual(result["ONEBOT_URL"], "http://external:9000")
            self.assertEqual(result["NAPCAT_ENABLED"], "false")
            self.assertEqual(settings(root, {"ONEBOT_URL": "http://replacement:9100"})["ONEBOT_URL"],
                             "http://replacement:9100")

    def test_invalid_flag_fails_explicitly(self):
        with tempfile.TemporaryDirectory() as path:
            with self.assertRaises(ValueError):
                settings(Path(path), {"NAPCAT_ENABLED": "maybe"})

    def test_colima_prefers_existing_legacy_data(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            self.assertEqual(colima_profile(root), "catbot")
            (root / "catbot").mkdir()
            self.assertEqual(colima_profile(root), "catbot")
            (root / "secretary").mkdir()
            self.assertEqual(colima_profile(root), "secretary")


class InitEnvTest(unittest.TestCase):
    def initialize(self, root):
        (root / "scripts").mkdir(exist_ok=True)
        for name in ("init-env.py", "deployment.py"):
            shutil.copy(ROOT / "scripts" / name, root / "scripts" / name)
        shutil.copy(ROOT / ".env.example", root / ".env.example")
        env = {key: value for key, value in os.environ.items()
               if key not in ("COMPOSE_PROJECT_NAME", "NAPCAT_ENABLED", "ONEBOT_URL")}
        env["NAPCAT_ENABLED"] = "false"
        subprocess.run([sys.executable, str(root / "scripts" / "init-env.py")],
                       env=env, check=True, capture_output=True)
        return settings(root, {})

    def test_new_config_defaults_to_catbot(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            values = self.initialize(root)
            self.assertEqual(values["COMPOSE_PROJECT_NAME"], "catbot")
            self.assertEqual((root / ".env").stat().st_mode & 0o777, 0o600)

    def test_upgrade_keeps_keys_and_legacy_volume_identity(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            original = "MASTER_KEY=fixture-key\nADMIN_PASSWORD=fixture-password\n"
            (root / ".env").write_text(original)
            values = self.initialize(root)
            self.assertEqual(values["COMPOSE_PROJECT_NAME"], "secretary")
            self.assertTrue((root / ".env").read_text().startswith(original))
            saved = (root / ".env").read_text()
            self.initialize(root)
            self.assertEqual((root / ".env").read_text(), saved)

    def test_upgrade_preserves_custom_project(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / ".env").write_text("COMPOSE_PROJECT_NAME=personal-cat\n")
            self.assertEqual(self.initialize(root)["COMPOSE_PROJECT_NAME"], "personal-cat")


class ComposeLauncherTest(unittest.TestCase):
    def test_launcher_passes_resolved_project_to_every_compose_call(self):
        spec = importlib.util.spec_from_file_location("compose_launcher", ROOT / "scripts" / "compose.py")
        launcher = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(launcher)
        for project in ("secretary", "catbot", "my-cat"):
            with self.subTest(project=project):
                values = {"NAPCAT_ENABLED": "false", "ONEBOT_URL": "http://external:3000",
                          "COMPOSE_PROJECT_NAME": project}
                with mock.patch.object(launcher, "settings", return_value=values), \
                     mock.patch.object(launcher.sys, "argv", ["compose", "up", "-d"]), \
                     mock.patch.object(launcher.subprocess, "run", return_value=mock.Mock(returncode=0)) as run, \
                     mock.patch.object(launcher.os, "chdir"), \
                     mock.patch.object(launcher.os, "execvpe") as execute:
                    launcher.main()
                stop = run.call_args_list[1]
                self.assertEqual(stop.kwargs["env"]["COMPOSE_PROJECT_NAME"], project)
                self.assertEqual(stop.args[0][-4:], ["--profile", "napcat", "stop", "napcat"])
                self.assertEqual(execute.call_args.args[2]["COMPOSE_PROJECT_NAME"], project)
                self.assertEqual(execute.call_args.args[1], ["docker", "compose", "up", "-d"])


if __name__ == "__main__":
    unittest.main()
