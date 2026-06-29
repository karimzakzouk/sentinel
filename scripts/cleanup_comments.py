#!/usr/bin/env python3
"""
Sentinel codebase refactoring script.
Removes version-history comments, dead code, and redundant comments.
"""
import os
import re
import sys

VERSION_COMMENT_PATTERNS = [
    re.compile(r'^\s*//\s*v[34]\.\d+\.\d+.*$', re.MULTILINE),
    re.compile(r'^\s*//\s*v[34]\.\d+\.\d+\s*[—-].*$', re.MULTILINE),
    re.compile(r'^\s*//\s*audit #\d+.*$', re.MULTILINE),
    re.compile(r'^\s*//\s*was (hardcoded|missing|broken|dead code).*$', re.MULTILINE),
    re.compile(r'^\s*//\s*CRITICAL audit.*$', re.MULTILINE),
    re.compile(r'\s*//\s*\(audit #\d+\)\s*$', re.MULTILINE),
    re.compile(r'\s*//\s*v[34]\.\d+\.\d+.*$', re.MULTILINE),
    re.compile(r'^\s*//\s*was .* in .*\.\s*$', re.MULTILINE),
    re.compile(r'^\s*//\s*Removed .*$', re.MULTILINE),
    re.compile(r'^\s*//\s*Replaces .*$', re.MULTILINE),
    re.compile(r'^\s*//\s*Now uses .*$', re.MULTILINE),
    re.compile(r'^\s*//\s*Now configurable.*$', re.MULTILINE),
    re.compile(r'^\s*//\s*default; override via.*$', re.MULTILINE),
    re.compile(r'^\s*//\s*Was:.*$', re.MULTILINE),
    re.compile(r'^\s*//\s*was accidentally.*$', re.MULTILINE),
    re.compile(r'^\s*//\s*was dead code.*$', re.MULTILINE),
    re.compile(r'^\s*//\s*Claude\'s review.*$', re.MULTILINE),
]

def clean_file(filepath):
    with open(filepath, 'r') as f:
        content = f.read()
    original_lines = content.count('\n')
    for pattern in VERSION_COMMENT_PATTERNS:
        content = pattern.sub('', content)
    content = re.sub(r'\n{3,}', '\n\n', content)
    content = re.sub(r'[ \t]+$', '', content, flags=re.MULTILINE)
    content = re.sub(r'\n\n(\s*\})', r'\n\1', content)
    new_lines = content.count('\n')
    removed = original_lines - new_lines
    if removed > 0:
        with open(filepath, 'w') as f:
            f.write(content)
    return removed

def main():
    base_dir = sys.argv[1] if len(sys.argv) > 1 else '.'
    total_removed = 0
    files_cleaned = 0
    for root, dirs, files in os.walk(base_dir):
        if 'vendor' in root or '.git' in root or 'node_modules' in root:
            continue
        for fname in files:
            if fname.endswith('.go'):
                filepath = os.path.join(root, fname)
                removed = clean_file(filepath)
                if removed > 0:
                    files_cleaned += 1
                    total_removed += removed
                    print(f"  {filepath}: -{removed} lines")
    print(f"\nTotal: {files_cleaned} files cleaned, {total_removed} comment lines removed")

if __name__ == '__main__':
    main()
