"""Fail-closed probe regressions; no desktop or pyatspi installation needed."""
import contextlib
import importlib.util
import io
import json
import pathlib
import sys
import types
import unittest
from unittest.mock import Mock, patch


class Node:
    def __init__(self, name='', children=(), broken=False):
        self.name = name
        self.children = children
        self.broken = broken
        self.action = Mock(nActions=1)
        self.action.doAction.return_value = True
        self.editable = Mock()
        self.editable.setTextContents.return_value = True

    def getRoleName(self):
        if self.broken:
            raise RuntimeError('window disappeared')
        return 'button'

    def getState(self):
        return types.SimpleNamespace(contains=lambda _: True)

    def queryComponent(self):
        raise NotImplementedError

    def queryAction(self):
        return self.action

    def queryEditableText(self):
        return self.editable

    def __iter__(self):
        return iter(self.children)


class ProbeTests(unittest.TestCase):
    def run_probe(self, children, action=('--click',), clock=None):
        fake = types.SimpleNamespace(STATE_SHOWING=1, DESKTOP_COORDS=0,
            Registry=types.SimpleNamespace(getDesktop=lambda _: [Node('fake-app', children)]))
        spec = importlib.util.spec_from_file_location('probe_under_test',
            pathlib.Path(__file__).with_name('a11y_probe.py'))
        probe = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, pyatspi=fake):
            spec.loader.exec_module(probe)
        output, errors = io.StringIO(), io.StringIO()
        with patch.object(sys, 'argv', ['probe', '--app', 'fake-app', '--role', 'button',
                                      '--name', 'Save', *action]), \
                patch.object(probe.os, 'getuid', return_value=1000), \
                patch.object(probe.pathlib.Path, 'glob', return_value=[]), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors), \
                contextlib.ExitStack() as stack:
            if clock is not None:
                stack.enter_context(patch.object(probe.time, 'monotonic', side_effect=clock))
            try:
                probe.main()
            except SystemExit as exc:
                return exc.code, errors.getvalue()
        return 0, json.loads(output.getvalue())

    def test_complete_unique_match_acts(self):
        target = Node('Save')
        code, result = self.run_probe([target])
        self.assertEqual(code, 0)
        self.assertFalse(result['incomplete'])
        self.assertTrue(result['accepted'])
        target.action.doAction.assert_called_once_with(0)

    def test_duplicate_match_refuses(self):
        first, second = Node('Save'), Node('Save')
        code, error = self.run_probe([first, second])
        self.assertEqual(code, 2)
        self.assertIn('exactly one', error)
        first.action.doAction.assert_not_called()
        second.action.doAction.assert_not_called()

    def assert_incomplete_refused(self, rest, clock=None):
        for action in [('--click',), ('--set-text', 'fake text')]:
            target = Node('Save')
            code, error = self.run_probe([target, *rest], action, clock)
            self.assertEqual(code, 2)
            self.assertIn('traversal was incomplete', error)
            target.action.doAction.assert_not_called()
            target.editable.setTextContents.assert_not_called()

    def test_depth_limit_refuses_even_one_visible_match(self):
        hidden_duplicate = Node('Save')
        for _ in range(31):
            hidden_duplicate = Node(children=[hidden_duplicate])
        self.assert_incomplete_refused([hidden_duplicate])

    def test_node_limit_refuses_even_one_visible_match(self):
        self.assert_incomplete_refused([Node() for _ in range(2000)] + [Node('Save')])

    def test_time_limit_refuses_even_one_visible_match(self):
        # Start, app and first match succeed; remaining subtree exceeds deadline.
        self.assert_incomplete_refused([Node('Save')], [0, 0, 0, 4, 4])

    def test_disappearing_node_refuses_even_one_visible_match(self):
        self.assert_incomplete_refused([Node(broken=True)])

    def test_inspection_marks_partial_result(self):
        code, result = self.run_probe([Node('Save'), Node(broken=True)], action=())
        self.assertEqual(code, 0)
        self.assertTrue(result['incomplete'])
        self.assertNotIn('accepted', result)


if __name__ == '__main__':
    unittest.main()
