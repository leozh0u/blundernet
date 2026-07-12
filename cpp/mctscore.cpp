// blundercore: the MCTS tree in C++, exposed to Python via pybind11.
//
// Division of labor: this module owns the search tree (selection via PUCT,
// virtual loss, expansion bookkeeping, backpropagation) in flat cache-friendly
// arrays. Python owns chess rules and the neural net. The API is batched on
// purpose: select_batch() returns many leaves at once so Python can evaluate
// them through the network in a single forward pass.
//
// Values are always stored from the perspective of the side to move at that
// node; the sign flips once per ply on backprop.

#include <pybind11/pybind11.h>
#include <pybind11/numpy.h>
#include <pybind11/stl.h>

#include <cmath>
#include <cstdint>
#include <vector>

namespace py = pybind11;

struct Tree {
    // node storage (struct-of-arrays)
    std::vector<float>    prior;
    std::vector<int32_t>  visits;
    std::vector<float>    value_sum;
    std::vector<int32_t>  vloss;        // virtual-loss count
    std::vector<int32_t>  first_child;  // -1 while unexpanded
    std::vector<int32_t>  num_children;
    std::vector<int32_t>  parent;
    std::vector<int32_t>  parent_slot;  // index among the parent's children

    float c_puct;

    explicit Tree(float c) : c_puct(c) { new_node(1.0f, -1, -1); }

    int32_t new_node(float p, int32_t par, int32_t slot) {
        prior.push_back(p);
        visits.push_back(0);
        value_sum.push_back(0.0f);
        vloss.push_back(0);
        first_child.push_back(-1);
        num_children.push_back(0);
        parent.push_back(par);
        parent_slot.push_back(slot);
        return static_cast<int32_t>(prior.size()) - 1;
    }

    bool expanded(int32_t n) const { return first_child[n] >= 0; }

    float q(int32_t n) const {
        int32_t v = visits[n] + vloss[n];
        // Virtual loss discourages other batch members from piling onto the
        // same path. Node values are from the node mover's own perspective
        // and the PARENT negates q when scoring — so a loss-for-the-parent
        // must be stored as a WIN (+1) from this node's perspective.
        return v ? (value_sum[n] + static_cast<float>(vloss[n])) / v : 0.0f;
    }

    // Walk from the root to an unexpanded leaf via PUCT, applying virtual
    // loss along the way. The path is a list of (parent_id, slot, child_id)
    // triples so Python can replay moves and track node ids without ever
    // reading tree internals.
    std::pair<int32_t, std::vector<std::tuple<int32_t, int32_t, int32_t>>>
    select_one() {
        int32_t node = 0;
        std::vector<std::tuple<int32_t, int32_t, int32_t>> path;
        while (expanded(node) && num_children[node] > 0) {
            const float sqrt_n =
                std::sqrt(static_cast<float>(visits[node] + vloss[node] + 1));
            const int32_t base = first_child[node];
            int32_t best_slot = 0;
            float best_score = -1e30f;
            for (int32_t s = 0; s < num_children[node]; ++s) {
                const int32_t c = base + s;
                const float u =
                    c_puct * prior[c] * sqrt_n / (1.0f + visits[c] + vloss[c]);
                const float score = -q(c) + u;  // child q is the child mover's view
                if (score > best_score) { best_score = score; best_slot = s; }
            }
            const int32_t child = base + best_slot;
            vloss[child] += 1;
            path.emplace_back(node, best_slot, child);
            node = child;
        }
        return {node, path};
    }

    // Collect up to `n` distinct leaves for one batched evaluation.
    // A duplicate leaf (paths converged) ends the collection early.
    std::pair<std::vector<int32_t>,
              std::vector<std::vector<std::tuple<int32_t, int32_t, int32_t>>>>
    select_batch(int n) {
        std::vector<int32_t> leaves;
        std::vector<std::vector<std::tuple<int32_t, int32_t, int32_t>>> paths;
        for (int i = 0; i < n; ++i) {
            auto [leaf, path] = select_one();
            bool dup = false;
            for (int32_t seen : leaves)
                if (seen == leaf) { dup = true; break; }
            if (dup) { undo_vloss(leaf); break; }
            leaves.push_back(leaf);
            paths.push_back(std::move(path));
        }
        return {leaves, paths};
    }

    void undo_vloss(int32_t node) {
        while (node > 0) { vloss[node] -= 1; node = parent[node]; }
    }

    // Attach children with the given priors, then backprop the leaf value.
    void expand_backprop(int32_t node, const std::vector<float>& priors,
                         float value) {
        if (!expanded(node) && !priors.empty()) {
            first_child[node] = static_cast<int32_t>(prior.size());
            num_children[node] = static_cast<int32_t>(priors.size());
            for (int32_t s = 0; s < static_cast<int32_t>(priors.size()); ++s)
                new_node(priors[s], node, s);
        }
        backprop(node, value);
    }

    // Terminal leaf: no children, just propagate the game result.
    void backprop(int32_t node, float value) {
        while (node >= 0) {
            visits[node] += 1;
            value_sum[node] += value;
            if (node > 0) vloss[node] -= 1;
            value = -value;
            node = parent[node];
        }
    }

    std::vector<int32_t> root_child_visits() const {
        std::vector<int32_t> out(num_children[0]);
        for (int32_t s = 0; s < num_children[0]; ++s)
            out[s] = visits[first_child[0] + s];
        return out;
    }

    void add_root_noise(const std::vector<float>& noise, float eps) {
        for (int32_t s = 0; s < num_children[0]; ++s) {
            const int32_t c = first_child[0] + s;
            prior[c] = (1.0f - eps) * prior[c] + eps * noise[s];
        }
    }

    int size() const { return static_cast<int>(prior.size()); }
    int root_visits() const { return visits[0]; }
};

PYBIND11_MODULE(blundercore, m) {
    m.doc() = "C++ MCTS tree core (PUCT, virtual loss, batched selection)";
    py::class_<Tree>(m, "Tree")
        .def(py::init<float>(), py::arg("c_puct") = 1.5f)
        .def("select_batch", &Tree::select_batch, py::arg("n"))
        .def("expand_backprop", &Tree::expand_backprop,
             py::arg("node"), py::arg("priors"), py::arg("value"))
        .def("backprop", &Tree::backprop, py::arg("node"), py::arg("value"))
        .def("root_child_visits", &Tree::root_child_visits)
        .def("add_root_noise", &Tree::add_root_noise,
             py::arg("noise"), py::arg("eps"))
        .def("size", &Tree::size)
        .def("root_visits", &Tree::root_visits);
}
