import unittest
from mgar_agents.core.claim import Claim


class ClaimTests(unittest.TestCase):
    def test_claim_is_immutable_and_fenced_identity_checked(self):
        c = Claim(task_id="t", run_id="r", owner="w", role="research", epoch=1, revision=2, plan_version=1)
        c.require_identity("w", "research")
        with self.assertRaises(PermissionError):
            c.require_identity("other", "research")
        with self.assertRaises(AttributeError):
            c.epoch = 0

    def test_bad_epoch_and_role_rejected(self):
        for epoch, role in [(0, "research"), (1, "admin")]:
            with self.assertRaises(ValueError):
                Claim(task_id="t", run_id="r", owner="w", role=role, epoch=epoch, revision=0, plan_version=1)
