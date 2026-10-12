"""Extract fixed 6b9826 dynamic-tool shapes; no network or schema discovery."""
import hashlib
import json
import pathlib
import sys

source, output = map(pathlib.Path, sys.argv[1:])
names = ["DynamicToolCallParams.json", "DynamicToolCallResponse.json", "ThreadStartParams.json", "ItemCompletedNotification.json"]
raw = {name: (source / name).read_bytes() for name in names}
expected = {
    "DynamicToolCallParams.json": "401bba20cfbd95762bef0467d840430c46be53369093ad9f26425ba757e34efc",
    "DynamicToolCallResponse.json": "abb082cad67f11fcc98ba75f2eff75d7d1723af0c657655329b83ff160451a02",
    "ThreadStartParams.json": "792e2f32e37cece971bd616664ea2053741acbed4e9c92e9d1766427718f2ecd",
    "ItemCompletedNotification.json": "69aba3fe5f72f38bf5c541e7e2c09de40778abe65ff969d9fc73372037812091",
}
if any(hashlib.sha256(content).hexdigest() != expected[name] for name, content in raw.items()):
    raise SystemExit("fixed schema identity mismatch")
docs = {name: json.loads(content) for name, content in raw.items()}
definitions = {}

def extract(value, document):
    if isinstance(value, list):
        return [extract(v, document) for v in value]
    if not isinstance(value, dict):
        return value
    result = {}
    for key, val in value.items():
        if key in ("description", "title", "$schema") and isinstance(val, str):
            continue
        if key == "$ref":
            name = val.removeprefix("#/definitions/")
            target = "host-tools::" + name
            if target not in definitions:
                definitions[target] = None
                definitions[target] = extract(document["definitions"][name], document)
            result[key] = "#/definitions/" + target
        elif key != "definitions":
            result[key] = extract(val, document)
    return result

item_doc = docs["ItemCompletedNotification.json"]
item = next(v for v in item_doc["definitions"]["ThreadItem"]["oneOf"] if v["properties"]["type"].get("enum") == ["dynamicToolCall"])
start = docs["ThreadStartParams.json"]
result = {
    "sourceCommit": "6b9826e3aa83b1a5947db50f4332cb9c65f1b340",
    "sourceSHA256": {name: hashlib.sha256(content).hexdigest() for name, content in raw.items()},
    "request": extract(docs[names[0]], docs[names[0]]),
    "response": extract(docs[names[1]], docs[names[1]]),
    "item": extract(item, item_doc),
    "spec": extract(start["definitions"]["DynamicToolSpec"], start),
    "definitions": definitions,
}
output.write_text(json.dumps(result, sort_keys=True, separators=(",", ":")) + "\n")
