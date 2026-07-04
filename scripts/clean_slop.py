#!/usr/bin/env python3
"""Mechanical AI slop comment cleanup for Sentinel Go codebase.
Removes version tags, audit IDs, Prow references, and "Was:" archaeology
from comments. Does NOT touch code — only comment text."""
import re, os, sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent

# Patterns to clean from comment lines (not delete the line, just clean the slop part)
PATTERNS = [
    # "v3.11.0:" / "v4.0.0:" / "v5.9.3:" version tags → remove the tag
    (r'(//.*?)(\s*v[345]\.\d+\.\d+:\s*)', r'\1'),
    # "H1 fix:" / "H2 fix:" / "M13 fix:" / "M9 fix:" / "H4 fix:" / "H6 fix:" / "H30" / "H4.8" → remove
    (r'(//.*?)(\s*[HM]\d+(?:\.\d+)?\s+fix:?\s*)', r'\1'),
    # "audit #N" / "audit finding #N" → remove
    (r'(//.*?)(\s*audit\s+(?:finding\s+)?#\d+\s*)', r'\1'),
    # "LEAK-1" / "obs-1" → remove
    (r'(//.*?)(\s*(?:LEAK-\d+|obs-\d+)\s*)', r'\1'),
    # "Prow's #N complaint" → remove the Prow reference
    (r"(//.*?)(\s*Prow's #\d+\s+\w+\s*)", r'\1'),
    # "(fixes LEAK-1 from the architecture audit)" → remove
    (r'(//.*?)(\s*\(fixes LEAK-\d+ from the architecture audit\)\s*)', r'\1'),
    # "( + obs-1)" / "(obs-1)" → remove
    (r'(//.*?)(\s*\(\s*(?:\+\s*)?obs-\d+\)\s*)', r'\1'),
]

# Lines to delete entirely (standalone slop lines)
DELETE_LINES = [
    r'^\s*//\s*(?:missing\s+\w+\s+import\s+fix|strings\s+import\s+guard)\s*$',
    r'^\s*//\s*unused import guards?\s*$',
    r'^\s*//\s*\(MarshalJSON removed.*\)$',
    r'^\s*//\s*\(same behavior as v3\.\d+.*\)\s*$',
]

def clean_file(path):
    """Clean slop from a single Go file. Returns (lines_removed, lines_cleaned)."""
    try:
        content = path.read_text()
    except:
        return 0, 0
    
    original = content
    lines = content.split('\n')
    new_lines = []
    removed = 0
    cleaned = 0
    
    for line in lines:
        # Check if line should be deleted entirely
        should_delete = False
        for pattern in DELETE_LINES:
            if re.match(pattern, line):
                should_delete = True
                removed += 1
                break
        if should_delete:
            continue
        
        # Clean slop patterns from comment lines
        new_line = line
        if '//' in new_line:
            for pattern, repl in PATTERNS:
                new_line2 = re.sub(pattern, repl, new_line)
                if new_line2 != new_line:
                    cleaned += 1
                    new_line = new_line2
        
        # Skip empty comment lines that result from cleanup (// followed by nothing)
        if new_line.strip() == '//':
            removed += 1
            continue
        
        new_lines.append(new_line)
    
    new_content = '\n'.join(new_lines)
    if new_content != original:
        path.write_text(new_content)
    
    return removed, cleaned

def main():
    total_removed = 0
    total_cleaned = 0
    files_changed = 0
    
    for go_file in REPO.rglob("*.go"):
        if "vendor/" in str(go_file) or "node_modules/" in str(go_file):
            continue
        removed, cleaned = clean_file(go_file)
        if removed > 0 or cleaned > 0:
            files_changed += 1
            total_removed += removed
            total_cleaned += cleaned
            print(f"  {go_file.relative_to(REPO)}: {removed} deleted, {cleaned} cleaned")
    
    print(f"\nTotal: {files_changed} files, {total_removed} lines deleted, {total_cleaned} lines cleaned")

if __name__ == "__main__":
    main()
