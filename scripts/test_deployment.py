import tempfile
import unittest
from pathlib import Path

from deployment import settings


class DependencySettingsTest(unittest.TestCase):
    def test_upgrade_keeps_local_dependency_enabled(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / ".env").write_text("ADMIN_PASSWORD=fixture\n")
            result = settings(root, {})
            self.assertEqual(result["NAPCAT_ENABLED"], "true")
            self.assertEqual(result["ONEBOT_URL"], "http://napcat:3000")

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


if __name__ == "__main__":
    unittest.main()
