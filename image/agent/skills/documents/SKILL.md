---
name: documents
description: Read, write and convert Word, Excel, PowerPoint, PDF and scanned documents without a GUI.
---

# Documents

Work on files directly; never open a GUI app for this.

- Convert: `soffice --headless --convert-to pdf FILE` (or `docx`, `xlsx`, `odt`, `csv`), `pandoc IN -o OUT` for Markdown, HTML and docx.
- Excel: `python3` with `openpyxl` (`load_workbook(path, data_only=True)` for values, not formulas). Keep the user's formatting: change cells, do not rebuild the sheet.
- Word: `python3` with `docx` (python-docx). For a new document from text, write Markdown and convert with pandoc.
- PDF to text: `pdftotext` if installed, otherwise `soffice --headless --convert-to txt`; scans: `tesseract PAGE.png - -l eng+deu`.
- Search: `rg -i WORD` for text files; for documents index once with `recollindex` and query with `recollq WORDS`.

Rules:

1. Never overwrite the user's original. Write results to `~/outputs/` with a clear name.
2. After writing, read the result back (open it with openpyxl/python-docx, or convert to text) and check it before you report success.
3. Say in the chat which file you created and what changed.
