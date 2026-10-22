import unittest
from mgar_agents.provider import MockProvider, provider


class ProviderTests(unittest.TestCase):
    def test_mock_without_keys(self):
        self.assertEqual(MockProvider().complete("hello"), "mock:hello")

    def test_real_provider_denied(self):
        with self.assertRaises(PermissionError):
            provider("real")

    def test_default_offline(self):
        self.assertIsInstance(provider(), MockProvider)
