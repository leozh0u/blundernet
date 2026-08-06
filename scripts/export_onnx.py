"""Export a BlunderNet checkpoint to ONNX for the inference workers.

Usage:
    python scripts/export_onnx.py --repo ~/Projects/blundernet-engine --out models/blundernet.onnx

Run inside the blundernet venv (needs torch, onnx, onnxruntime).
Verifies ONNX Runtime output matches PyTorch to 1e-4 before writing is
considered successful, and reports single-position CPU latency.
"""
import argparse
import pathlib
import sys
import time

import numpy as np
import torch


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", required=True, help="path to the blundernet repo")
    ap.add_argument("--out", default="models/blundernet.onnx")
    ap.add_argument("--opset", type=int, default=17)
    args = ap.parse_args()

    repo = pathlib.Path(args.repo).expanduser()
    sys.path.insert(0, str(repo / "src"))
    from blundernet.model import BlunderNet  # noqa: E402
    from blundernet.encode import PLANES  # noqa: E402

    ckpt = torch.load(repo / "checkpoint" / "model.pt", map_location="cpu", weights_only=False)
    state = ckpt.get("model") if isinstance(ckpt, dict) and "model" in ckpt else ckpt
    if hasattr(state, "state_dict"):
        state = state.state_dict()

    model = BlunderNet()
    model.load_state_dict(state)
    model.eval()

    out_path = pathlib.Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)

    dummy = torch.zeros(1, PLANES, 8, 8)
    torch.onnx.export(
        model,
        dummy,
        str(out_path),
        input_names=["board"],
        output_names=["policy", "value"],
        dynamic_axes={"board": {0: "batch"}, "policy": {0: "batch"}, "value": {0: "batch"}},
        opset_version=args.opset,
    )

    # The dynamo exporter writes weights to a sidecar .data file; fold them
    # back in so the worker ships a single artifact.
    import onnx

    m = onnx.load(str(out_path))
    data_file = out_path.with_suffix(".onnx.data")
    onnx.save(m, str(out_path), save_as_external_data=False)
    data_file.unlink(missing_ok=True)

    # Parity check: same random inputs through both runtimes.
    import onnxruntime as ort

    sess = ort.InferenceSession(str(out_path), providers=["CPUExecutionProvider"])
    x = np.random.rand(4, PLANES, 8, 8).astype(np.float32)
    with torch.no_grad():
        ref_policy, ref_value = model(torch.from_numpy(x))
    got_policy, got_value = sess.run(None, {"board": x})
    np.testing.assert_allclose(ref_policy.numpy(), got_policy, atol=1e-4)
    np.testing.assert_allclose(ref_value.numpy(), got_value, atol=1e-4)

    # Single-position latency, the number the worker design hangs on.
    one = x[:1]
    for _ in range(10):
        sess.run(None, {"board": one})
    n = 200
    t0 = time.perf_counter()
    for _ in range(n):
        sess.run(None, {"board": one})
    ms = (time.perf_counter() - t0) / n * 1000
    size_kb = out_path.stat().st_size // 1024
    print(f"ok: {out_path} ({size_kb} KB), parity 1e-4, single-position CPU {ms:.2f} ms")


if __name__ == "__main__":
    main()
