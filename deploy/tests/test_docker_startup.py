"""Docker startup regressions, with no access to a real daemon or VM."""
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("docker_startup_under_test", SCRIPTS / "start_docker.py")
startup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(startup)


class DockerStartupTest(unittest.TestCase):
    def patch(self, *args, **kwargs):
        patcher = mock.patch(*args, **kwargs)
        self.addCleanup(patcher.stop)
        return patcher.start()

    def patch_object(self, *args, **kwargs):
        patcher = mock.patch.object(*args, **kwargs)
        self.addCleanup(patcher.stop)
        return patcher.start()

    def setUp(self):
        patcher = mock.patch.dict(os.environ, {}, clear=True)
        patcher.start()
        self.addCleanup(patcher.stop)
        # All subprocess calls are intercepted, including unexpected calls.
        self.run = self.patch_object(startup.subprocess, "run",
                                     return_value=subprocess.CompletedProcess([], 0))
        self.output = self.patch("builtins.print")
        self.patch_object(startup.shutil, "which", side_effect=lambda name: "/fixture/bin/" + name)

    def fake_clock(self):
        clock = {"now": 0.0}
        self.patch_object(startup.time, "monotonic", side_effect=lambda: clock["now"])

        def advance(seconds):
            clock["now"] += seconds

        self.patch_object(startup.time, "sleep", side_effect=advance)
        return clock

    def test_probe_targets_selected_context_with_bounded_timeout(self):
        self.assertTrue(startup.probe("colima-secretary"))
        self.run.assert_called_once_with(
            ["docker", "--context", "colima-secretary", "info"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=startup.PROBE_TIMEOUT)

    def test_probe_without_override_uses_current_endpoint(self):
        self.assertTrue(startup.probe(timeout=3))
        self.run.assert_called_once_with(
            ["docker", "info"], stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL, timeout=3)

    def test_probe_timeout_is_unavailable_instead_of_an_unbounded_wait(self):
        self.run.side_effect = subprocess.TimeoutExpired(["docker", "info"], 5)
        self.assertFalse(startup.probe())

    def test_probe_nonzero_exit_is_unavailable(self):
        self.run.return_value = subprocess.CompletedProcess([], 1)
        self.assertFalse(startup.probe())

    def test_existing_current_daemon_is_reused_without_selecting_a_context(self):
        self.patch_object(startup, "probe", return_value=True)
        context = self.patch_object(startup, "current_context")
        prepare = self.patch_object(startup, "prepare_colima")
        startup.ensure_docker()
        context.assert_not_called()
        prepare.assert_not_called()
        self.run.assert_not_called()

    def test_running_project_vm_is_selected_after_stale_default_context(self):
        # Regression: `colima start` may say "already running" without activating
        # its Docker context, leaving later Make recipes on a dead default.
        probe = self.patch_object(startup, "probe", side_effect=lambda context=None: context == "colima-secretary")
        self.patch_object(startup, "current_context", return_value="default")
        self.patch_object(startup, "colima_profile", return_value="secretary")
        startup.ensure_docker()
        self.assertEqual(probe.call_args_list, [mock.call(), mock.call("colima-secretary")])
        self.run.assert_called_once_with(
            ["docker", "context", "use", "colima-secretary"],
            check=True, stdout=subprocess.DEVNULL, timeout=startup.PROBE_TIMEOUT)

    def test_explicit_colima_context_is_reused_without_global_activation(self):
        os.environ["DOCKER_CONTEXT"] = "colima-other"
        self.patch_object(startup, "probe", side_effect=lambda context=None: context == "colima-other")
        self.patch_object(startup, "current_context", return_value="colima-other")
        activate = self.patch_object(startup, "activate_context")
        startup.ensure_docker()
        activate.assert_not_called()
        self.run.assert_not_called()

    def test_explicit_host_failure_does_not_fall_back_to_colima(self):
        os.environ["DOCKER_HOST"] = "tcp://fixture.invalid:2375"
        self.patch_object(startup, "probe", return_value=False)
        context = self.patch_object(startup, "current_context")
        prepare = self.patch_object(startup, "prepare_colima")
        with self.assertRaisesRegex(RuntimeError, "DOCKER_HOST"):
            startup.ensure_docker()
        context.assert_not_called()
        prepare.assert_not_called()
        self.run.assert_not_called()

    def test_explicit_context_takes_precedence_over_host(self):
        os.environ.update(DOCKER_CONTEXT="colima-other", DOCKER_HOST="tcp://fixture.invalid:2375")
        self.patch_object(startup, "probe", return_value=False)
        self.patch_object(startup, "current_context", return_value="colima-other")
        prepare = self.patch_object(startup, "prepare_colima")
        startup.ensure_docker()
        prepare.assert_called_once_with("other")

    def test_explicit_default_context_does_not_select_project_colima(self):
        os.environ["DOCKER_CONTEXT"] = "default"
        self.patch_object(startup, "probe", return_value=False)
        self.patch_object(startup, "current_context", return_value="default")
        self.patch_object(startup.Path, "is_dir", return_value=False)
        prepare = self.patch_object(startup, "prepare_colima")
        with self.assertRaises(RuntimeError):
            startup.ensure_docker()
        prepare.assert_not_called()
        self.run.assert_not_called()

    def test_unknown_unavailable_context_does_not_switch_daemons(self):
        self.patch_object(startup, "probe", return_value=False)
        self.patch_object(startup, "current_context", return_value="remote-work")
        prepare = self.patch_object(startup, "prepare_colima")
        with self.assertRaisesRegex(RuntimeError, "remote-work"):
            startup.ensure_docker()
        prepare.assert_not_called()
        self.run.assert_not_called()

    def test_missing_docker_fails_before_probe(self):
        self.patch_object(startup.shutil, "which", return_value=None)
        probe = self.patch_object(startup, "probe")
        with self.assertRaisesRegex(RuntimeError, "缺少 Docker"):
            startup.ensure_docker()
        probe.assert_not_called()

    def test_missing_colima_has_actionable_error(self):
        self.patch_object(startup.shutil, "which", side_effect=lambda name: "/fixture/docker" if name == "docker" else None)
        self.patch_object(startup, "probe", return_value=False)
        self.patch_object(startup, "current_context", return_value="colima-secretary")
        with self.assertRaisesRegex(RuntimeError, "Colima"):
            startup.ensure_docker()
        self.run.assert_not_called()

    def test_project_start_waits_for_its_own_context_before_activation(self):
        self.patch_object(startup, "probe", return_value=False)
        wait = self.patch_object(startup, "wait_ready")
        activate = self.patch_object(startup, "activate_context")
        startup.prepare_colima("secretary", automatic=True)
        self.run.assert_called_once_with(
            ["colima", "--profile", "secretary", "start", "--activate=false",
             "--cpu", "2", "--memory", "4", "--disk", "60"], check=True, timeout=180)
        wait.assert_called_once_with("colima-secretary", activate=True)
        activate.assert_not_called()

    def test_explicit_colima_start_does_not_override_resource_settings(self):
        self.patch_object(startup, "probe", return_value=False)
        wait = self.patch_object(startup, "wait_ready")
        startup.prepare_colima("default")
        self.run.assert_called_once_with(
            ["colima", "--profile", "default", "start", "--activate=false"],
            check=True, timeout=180)
        wait.assert_called_once_with("colima", activate=False)

    def test_failed_colima_start_does_not_wait_or_activate(self):
        self.patch_object(startup, "probe", return_value=False)
        self.run.side_effect = subprocess.CalledProcessError(1, ["colima", "start"])
        wait = self.patch_object(startup, "wait_ready")
        activate = self.patch_object(startup, "activate_context")
        with self.assertRaises(subprocess.CalledProcessError):
            startup.prepare_colima("secretary", automatic=True)
        wait.assert_not_called()
        activate.assert_not_called()

    def test_wait_activates_only_after_target_is_healthy(self):
        self.fake_clock()
        activate = self.patch_object(startup, "activate_context")
        health = iter([False, True])

        def probe(context, timeout):
            self.assertEqual(context, "colima-secretary")
            activate.assert_not_called()
            return next(health)

        self.patch_object(startup, "probe", side_effect=probe)
        startup.wait_ready("colima-secretary", activate=True)
        activate.assert_called_once_with("colima-secretary")

    def test_wait_deadline_includes_slow_probe_time_and_never_activates_failure(self):
        clock = self.fake_clock()
        timeouts = []
        activate = self.patch_object(startup, "activate_context")

        def slow_probe(context, timeout):
            self.assertEqual(context, "colima-secretary")
            timeouts.append(timeout)
            clock["now"] += timeout
            return False

        self.patch_object(startup, "probe", side_effect=slow_probe)
        with self.assertRaisesRegex(RuntimeError, "colima-secretary"):
            startup.wait_ready("colima-secretary", activate=True)
        self.assertEqual(clock["now"], startup.WAIT_TIMEOUT)
        self.assertTrue(all(0 < timeout <= startup.PROBE_TIMEOUT for timeout in timeouts))
        self.assertLess(timeouts[-1], startup.PROBE_TIMEOUT)
        activate.assert_not_called()
        self.assertGreater(self.output.call_count, 1)

    def test_wait_without_activation_preserves_current_context(self):
        self.fake_clock()
        self.patch_object(startup, "probe", return_value=True)
        activate = self.patch_object(startup, "activate_context")
        startup.wait_ready()
        activate.assert_not_called()

    def test_main_reports_timeout_and_returns_failure(self):
        self.patch_object(startup, "ensure_docker", side_effect=subprocess.TimeoutExpired(["docker", "info"], 5))
        self.assertEqual(startup.main(), 1)
        self.assertEqual(self.output.call_args.kwargs["file"], sys.stderr)


if __name__ == "__main__":
    unittest.main()
