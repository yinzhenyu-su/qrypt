#!/usr/bin/env python3
"""
Base64 Encoder/Decoder Tool
Based on Python's base64: https://github.com/python/cpython

Usage:
    python base64_encoder.py encode "Hello World"
    python base64_encoder.py decode "SGVsbG8gV29ybGQ="
    python base64_encoder.py encode --file image.png
"""

import argparse
import base64
import sys
from pathlib import Path

def encode_data(data: bytes, url_safe: bool = False) -> str:
    """Encode bytes to Base64."""
    if url_safe:
        return base64.urlsafe_b64encode(data).decode('ascii')
    return base64.b64encode(data).decode('ascii')

def decode_data(data: str, url_safe: bool = False) -> bytes:
    """Decode Base64 to bytes."""
    if url_safe:
        return base64.urlsafe_b64decode(data)
    return base64.b64decode(data)

def main():
    parser = argparse.ArgumentParser(description="Base64 encode/decode")
    subparsers = parser.add_subparsers(dest='command', required=True)
    
    # Encode
    p_enc = subparsers.add_parser('encode', help='Encode to Base64')
    p_enc.add_argument('text', nargs='?', help='Text to encode')
    p_enc.add_argument('--file', '-f', help='File to encode')
    p_enc.add_argument('--output', '-o', help='Output file')
    p_enc.add_argument('--url-safe', '-u', action='store_true')
    
    # Decode
    p_dec = subparsers.add_parser('decode', help='Decode from Base64')
    p_dec.add_argument('text', nargs='?', help='Base64 to decode')
    p_dec.add_argument('--file', '-f', help='File containing Base64')
    p_dec.add_argument('--output', '-o', help='Output file')
    p_dec.add_argument('--url-safe', '-u', action='store_true')
    
    args = parser.parse_args()
    
    if args.command == 'encode':
        if args.file:
            with open(args.file, 'rb') as f:
                data = f.read()
        elif args.text:
            data = args.text.encode('utf-8')
        else:
            data = sys.stdin.buffer.read()
        
        result = encode_data(data, args.url_safe)
        
        if args.output:
            with open(args.output, 'w') as f:
                f.write(result)
            print(f"✓ Encoded to {args.output}")
        else:
            print(result)
    
    elif args.command == 'decode':
        if args.file:
            with open(args.file, 'r') as f:
                data = f.read().strip()
        elif args.text:
            data = args.text
        else:
            data = sys.stdin.read().strip()
        
        result = decode_data(data, args.url_safe)
        
        if args.output:
            with open(args.output, 'wb') as f:
                f.write(result)
            print(f"✓ Decoded to {args.output}")
        else:
            try:
                print(result.decode('utf-8'))
            except UnicodeDecodeError:
                print(f"Binary data ({len(result)} bytes)")

if __name__ == "__main__":
    main()