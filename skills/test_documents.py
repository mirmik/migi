import importlib.machinery
import json
from pathlib import Path
import sys
import tempfile
import unittest

scripts = Path(__file__).parent / 'migi-file-exchange' / 'scripts'
sys.path.insert(0, str(scripts))
client = importlib.machinery.SourceFileLoader('migi_document', str(scripts / 'migi-document')).load_module()

class DocumentClientTests(unittest.TestCase):
    def test_html_preserves_math_and_list(self):
        p = client.NoteHTML()
        p.feed(r'<article><h1>Заметка</h1><p>A &amp; B<br>C</p><math>\frac{a}{b}</math><ul><li>Первое</li><li>Второе</li></ul></article>')
        doc = p.document()
        self.assertEqual('A & B\nC', doc['blocks'][1]['text'])
        self.assertEqual(r'\frac{a}{b}', doc['blocks'][2]['latex'])
        self.assertEqual(['Первое','Второе'], doc['blocks'][3]['items'])
    def test_rejects_active_and_unsupported_markup(self):
        for html in ['<script>alert(1)</script>', '<p onclick="x">a</p>', '<p>x<math>y</math></p>', '<p>unclosed', '<ul><li><p>x</p></li></ul>']:
            with self.subTest(html=html), self.assertRaises(ValueError):
                p = client.NoteHTML(); p.feed(html); p.document()
    def test_retries_are_stable_and_changed_content_has_new_id(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)/'note.html'
            path.write_text('<h1>Title</h1><p>First</p>')
            a = client.prepare(path); self.assertEqual(a, client.prepare(path))
            path.write_text('<h1>Title</h1><p>Second</p>')
            self.assertNotEqual(json.loads(a)['document_id'],json.loads(client.prepare(path))['document_id'])
    def test_check_rejects_unknown_json_blocks(self):
        with self.assertRaises(ValueError):
            client.validate({'schema':1,'document_id':'a','title':'T','blocks':[{'type':'script','text':'x'}]})

if __name__ == '__main__': unittest.main()
