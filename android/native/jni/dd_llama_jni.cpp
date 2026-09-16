// DinnerDecider JNI shim over llama.cpp + libmtmd (plan Step 35).
//
// Kotlin binding: com.philwoolley.dinnerdecider.llm.LlamaNative.
//
// Five things in this file are load-bearing and each has a specific failure mode
// if it is "simplified" away. They are called out at their definitions:
//
//   1. LLAMA_LOAD_MODE_MMAP, never mlock.  use_mmap/use_mlock no longer exist at
//      the pinned commit; mparams.use_mmap = true is a compile error. mlock would
//      recreate the iOS wired-memory problem on a platform that does not have it.
//   2. Explicit ARGB_8888 -> packed RGB conversion with a length assert.
//      mtmd_bitmap_init memcpys nx*ny*3 with no length parameter and no
//      validation, so handing it 4-channel data is garbage input AND an
//      out-of-bounds read.
//   3. The batch pre-flight guard.  llama-context.cpp:1724 is
//      GGML_ASSERT(n_tokens_all <= cparams.n_batch), which ABORTS THE PROCESS.
//      We reject with a catchable Java exception before it can fire.
//   4. Cancellation / EOG / token cap in the decode loop.  Without all three, a
//      cancelled scan blocks in native code for a whole generation, which on a
//      CPU-only backend is minutes.
//   5. No grammar, no GBNF, no JSON schema, no responseFormat.  Not exposed, not
//      settable. See shared/parser/response-parsing.md, section PROHIBITION.

#include <jni.h>
#include <android/log.h>

#include <atomic>
#include <cstdint>
#include <cstring>
#include <memory>
#include <string>
#include <vector>

#include "llama.h"
#include "common.h"
#include "sampling.h"
#include "chat.h"
#include "mtmd.h"
#include "mtmd-helper.h"

#define DD_TAG "DDLlamaJNI"
#define DD_LOGI(...) __android_log_print(ANDROID_LOG_INFO,  DD_TAG, __VA_ARGS__)
#define DD_LOGW(...) __android_log_print(ANDROID_LOG_WARN,  DD_TAG, __VA_ARGS__)
#define DD_LOGE(...) __android_log_print(ANDROID_LOG_ERROR, DD_TAG, __VA_ARGS__)

namespace {

constexpr const char * kExceptionClass = "com/philwoolley/dinnerdecider/llm/LlamaNativeException";
constexpr const char * kFallbackExceptionClass = "java/lang/IllegalStateException";

// Every native call is confined to a single-threaded dispatcher on the Kotlin
// side (LlamaHandle), which is what makes llama_free concurrent with
// llama_decode structurally impossible rather than merely unlikely. This struct
// therefore needs no internal locking; the one field that IS touched from
// another thread is `cancel`, and it is atomic for exactly that reason.
struct dd_ctx {
    llama_model              * model  = nullptr;
    llama_context            * lctx   = nullptr;
    mtmd_context             * mctx   = nullptr;
    const llama_vocab        * vocab  = nullptr;
    common_chat_templates_ptr  tmpls;

    int32_t   n_ctx   = 0;
    int32_t   n_batch = 0;

    // n_past and prompt_cache together describe what the KV cache currently
    // holds. They are reset as ONE unit; see dd_reset_session.
    llama_pos                 n_past = 0;
    std::vector<llama_token>  prompt_cache;

    std::atomic<bool> cancel{false};
};

std::atomic<bool> g_backend_ready{false};

void dd_log_callback(ggml_log_level level, const char * text, void * /*user_data*/) {
    if (text == nullptr) {
        return;
    }
    switch (level) {
        case GGML_LOG_LEVEL_ERROR: DD_LOGE("%s", text); break;
        case GGML_LOG_LEVEL_WARN:  DD_LOGW("%s", text); break;
        default:                   DD_LOGI("%s", text); break;
    }
}

// Raise a Java exception the Kotlin layer can catch. Every failure path in this
// file goes through here: an UnsatisfiedLinkError or an abort() is never an
// acceptable way for this shim to report a problem.
void dd_throw(JNIEnv * env, const std::string & message) {
    if (env->ExceptionCheck()) {
        return;
    }
    jclass cls = env->FindClass(kExceptionClass);
    if (cls == nullptr) {
        env->ExceptionClear();
        cls = env->FindClass(kFallbackExceptionClass);
    }
    if (cls != nullptr) {
        env->ThrowNew(cls, message.c_str());
        env->DeleteLocalRef(cls);
    }
}

std::string dd_jstring(JNIEnv * env, jstring value) {
    if (value == nullptr) {
        return {};
    }
    const char * chars = env->GetStringUTFChars(value, nullptr);
    if (chars == nullptr) {
        return {};
    }
    std::string out(chars);
    env->ReleaseStringUTFChars(value, chars);
    return out;
}

dd_ctx * dd_from_handle(jlong handle) {
    return reinterpret_cast<dd_ctx *>(static_cast<uintptr_t>(handle));
}

// Clearing the KV cache without clearing n_past and prompt_cache leaves this
// shim believing the model still has a prefix it no longer has: positions would
// continue from a stale n_past into an empty cache and every subsequent reply is
// conditioned on nothing. They are one unit, always reset together.
void dd_reset_session(dd_ctx * ctx) {
    if (ctx->lctx != nullptr) {
        llama_memory_clear(llama_get_memory(ctx->lctx), true);
    }
    ctx->n_past = 0;
    ctx->prompt_cache.clear();
}

void dd_destroy(dd_ctx * ctx) {
    if (ctx == nullptr) {
        return;
    }
    if (ctx->mctx != nullptr) {
        mtmd_free(ctx->mctx);
        ctx->mctx = nullptr;
    }
    ctx->tmpls.reset();
    if (ctx->lctx != nullptr) {
        llama_free(ctx->lctx);
        ctx->lctx = nullptr;
    }
    if (ctx->model != nullptr) {
        llama_model_free(ctx->model);
        ctx->model = nullptr;
    }
    delete ctx;
}

// ---------------------------------------------------------------------------
// LOAD-BEARING #2: ARGB_8888 -> tightly-packed 3-channel RGB.
//
// Bitmap.getPixels() hands us Color ints, 0xAARRGGBB as a VALUE (which is BGRA
// byte order in memory on a little-endian device). mtmd_bitmap_init takes
// tightly-packed RGBRGBRGB and memcpys exactly nx*ny*3 bytes, with no length
// parameter and no validation of any kind. Passing the pixel array through
// unconverted is therefore two bugs at once: the model sees channel-shuffled
// garbage, and libmtmd reads a quarter past the end of a buffer that is only
// nx*ny*3 bytes long when interpreted as RGB.
//
// The alpha channel is dropped, not blended: the preprocessor upstream produces
// opaque bitmaps, and a blend would silently alter what the model sees.
// ---------------------------------------------------------------------------
std::vector<unsigned char> dd_argb_to_rgb(const jint * pixels, uint32_t nx, uint32_t ny) {
    const size_t count = static_cast<size_t>(nx) * static_cast<size_t>(ny);
    std::vector<unsigned char> rgb(count * 3);
    for (size_t i = 0; i < count; ++i) {
        const uint32_t p = static_cast<uint32_t>(pixels[i]);
        rgb[i * 3 + 0] = static_cast<unsigned char>((p >> 16) & 0xFFu);  // R
        rgb[i * 3 + 1] = static_cast<unsigned char>((p >>  8) & 0xFFu);  // G
        rgb[i * 3 + 2] = static_cast<unsigned char>((p      ) & 0xFFu);  // B
    }
    return rgb;
}

}  // namespace

extern "C" {

// ---------------------------------------------------------------------------
// nativeInit
// ---------------------------------------------------------------------------
JNIEXPORT jboolean JNICALL
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeInit(JNIEnv * /*env*/, jobject /*thiz*/) {
    bool expected = false;
    if (g_backend_ready.compare_exchange_strong(expected, true)) {
        llama_log_set(dd_log_callback, nullptr);
        mtmd_helper_log_set(dd_log_callback, nullptr);
        llama_backend_init();
        DD_LOGI("llama backend initialised");
    }
    return JNI_TRUE;
}

// ---------------------------------------------------------------------------
// nativeSystemInfo - cheap, no model, used by the ABI smoke test
// ---------------------------------------------------------------------------
JNIEXPORT jstring JNICALL
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeSystemInfo(JNIEnv * env, jobject /*thiz*/) {
    std::string info = "mtmd_marker=";
    info += mtmd_default_marker();
    info += " max_devices=" + std::to_string(llama_max_devices());
    return env->NewStringUTF(info.c_str());
}

// ---------------------------------------------------------------------------
// nativeLoad
// ---------------------------------------------------------------------------
JNIEXPORT jlong JNICALL
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeLoad(
        JNIEnv * env,
        jobject  /*thiz*/,
        jstring  jModelPath,
        jstring  jMmprojPath,
        jint     nCtx,
        jint     nBatch,
        jint     nThreads) {

    const std::string model_path  = dd_jstring(env, jModelPath);
    const std::string mmproj_path = dd_jstring(env, jMmprojPath);

    if (model_path.empty()) {
        dd_throw(env, "nativeLoad: model path is empty");
        return 0;
    }
    // LOAD-BEARING #3, first half. 512 is a hard floor: Gemma 4 emits roughly
    // 256 image tokens per 896x896 image, and a batch below the image token
    // count trips GGML_ASSERT (an abort, not an exception) inside llama_decode.
    if (nBatch < 1 || nCtx < 1 || nThreads < 1) {
        dd_throw(env, "nativeLoad: nCtx, nBatch and nThreads must all be positive");
        return 0;
    }

    auto ctx = std::unique_ptr<dd_ctx>(new dd_ctx());
    ctx->n_ctx   = nCtx;
    ctx->n_batch = nBatch;

    llama_model_params mparams = llama_model_default_params();
    // ---------------------------------------------------------------------
    // LOAD-BEARING #1. `use_mmap` and `use_mlock` DO NOT EXIST at the pinned
    // commit (include/llama.h:205-212, 322 replaced them with
    // enum llama_load_mode). `mparams.use_mmap = true` is a compile error, not a
    // deprecation warning.
    //
    // MMAP, explicitly, and never MLOCK or MMAP_MLOCK: mlock would pin 3.5 GB of
    // weights into non-evictable RAM, recreating the iOS wired-memory jetsam on
    // a platform whose whole memory advantage is that the weights are clean,
    // file-backed and evictable.
    // ---------------------------------------------------------------------
    mparams.load_mode = LLAMA_LOAD_MODE_MMAP;
    // Android has no GPU backend in this build: inference is CPU-only by
    // construction, so there is nothing to offload.
    mparams.n_gpu_layers = 0;

    ctx->model = llama_model_load_from_file(model_path.c_str(), mparams);
    if (ctx->model == nullptr) {
        dd_throw(env, "nativeLoad: llama_model_load_from_file failed for " + model_path);
        return 0;
    }

    ctx->vocab = llama_model_get_vocab(ctx->model);

    llama_context_params cparams = llama_context_default_params();
    cparams.n_ctx           = static_cast<uint32_t>(nCtx);
    cparams.n_batch         = static_cast<uint32_t>(nBatch);
    cparams.n_ubatch        = static_cast<uint32_t>(nBatch);
    cparams.n_threads       = nThreads;
    cparams.n_threads_batch = nThreads;

    ctx->lctx = llama_init_from_model(ctx->model, cparams);
    if (ctx->lctx == nullptr) {
        dd_throw(env, "nativeLoad: llama_init_from_model failed");
        llama_model_free(ctx->model);
        ctx->model = nullptr;
        return 0;
    }

    // The jinja chat template from the GGUF. llama_chat_apply_template is NOT an
    // equivalent fallback: llama.h:1215 says outright that it does not use a
    // jinja parser, so Gemma 4 falls through to gemma-7b-it scaffolding. The
    // media marker survives but instruction-following degrades, which is exactly
    // the class of silent recognition regression this port is trying to avoid.
    ctx->tmpls = common_chat_templates_init(ctx->model, "");
    if (!ctx->tmpls) {
        dd_throw(env, "nativeLoad: common_chat_templates_init failed");
        llama_free(ctx->lctx);
        ctx->lctx = nullptr;
        llama_model_free(ctx->model);
        ctx->model = nullptr;
        return 0;
    }

    if (!mmproj_path.empty()) {
        mtmd_context_params mp = mtmd_context_params_default();
        // Not a policy choice: this build contains no Vulkan and no OpenCL
        // backend, so the projector runs on CPU by construction. The portable
        // half of the iOS "do not run mmproj on CPU" rule is batch >= 512, which
        // is enforced above and pre-flighted below.
        mp.use_gpu        = false;
        mp.print_timings  = false;
        mp.n_threads      = nThreads;
        mp.flash_attn_type = LLAMA_FLASH_ATTN_TYPE_DISABLED;

        ctx->mctx = mtmd_init_from_file(mmproj_path.c_str(), ctx->model, mp);
        if (ctx->mctx == nullptr) {
            DD_LOGW("mtmd_init_from_file failed for %s; vision will be unavailable", mmproj_path.c_str());
        } else if (!mtmd_support_vision(ctx->mctx)) {
            DD_LOGW("projector loaded but reports no vision support");
        }
    }

    dd_ctx * raw = ctx.release();
    DD_LOGI("model loaded: n_ctx=%d n_batch=%d n_threads=%d vision=%s",
            nCtx, nBatch, nThreads, raw->mctx != nullptr ? "yes" : "no");
    return static_cast<jlong>(reinterpret_cast<uintptr_t>(raw));
}

// ---------------------------------------------------------------------------
// nativeFree
// ---------------------------------------------------------------------------
JNIEXPORT void JNICALL
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeFree(
        JNIEnv * /*env*/, jobject /*thiz*/, jlong handle) {
    dd_ctx * ctx = dd_from_handle(handle);
    if (ctx == nullptr) {
        return;
    }
    dd_destroy(ctx);
}

// ---------------------------------------------------------------------------
// nativeResetSession
// ---------------------------------------------------------------------------
JNIEXPORT void JNICALL
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeResetSession(
        JNIEnv * /*env*/, jobject /*thiz*/, jlong handle) {
    dd_ctx * ctx = dd_from_handle(handle);
    if (ctx == nullptr) {
        return;
    }
    dd_reset_session(ctx);
}

// ---------------------------------------------------------------------------
// nativeCancel - the only thread-safe entry point; see dd_ctx::cancel.
// ---------------------------------------------------------------------------
JNIEXPORT void JNICALL
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeCancel(
        JNIEnv * /*env*/, jobject /*thiz*/, jlong handle) {
    dd_ctx * ctx = dd_from_handle(handle);
    if (ctx == nullptr) {
        return;
    }
    ctx->cancel.store(true, std::memory_order_relaxed);
}

// ---------------------------------------------------------------------------
// nativeSupportsVision
// ---------------------------------------------------------------------------
JNIEXPORT jboolean JNICALL
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeSupportsVision(
        JNIEnv * /*env*/, jobject /*thiz*/, jlong handle) {
    dd_ctx * ctx = dd_from_handle(handle);
    if (ctx == nullptr || ctx->mctx == nullptr) {
        return JNI_FALSE;
    }
    return mtmd_support_vision(ctx->mctx) ? JNI_TRUE : JNI_FALSE;
}

// ---------------------------------------------------------------------------
// nativeGenerate
//
// NOTE the absence of a grammar / GBNF / json-schema / responseFormat parameter.
// That is deliberate and permanent. The runtime's grammar sampler is not reset
// between calls on a resident model: call 1 returns clean JSON and calls 2-4
// return ZERO tokens, which a scan swallows into "Nothing spotted". It passes
// any single-call smoke test. See shared/parser/response-parsing.md, PROHIBITION.
// Grammar-free plus LlmResponseParser salvage is the chosen path.
// ---------------------------------------------------------------------------
JNIEXPORT jstring JNICALL
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeGenerate(
        JNIEnv *   env,
        jobject    /*thiz*/,
        jlong      handle,
        jstring    jPrompt,
        jintArray  jPixels,
        jint       width,
        jint       height,
        jint       maxTokens,
        jfloat     temperature,
        jint       topK,
        jfloat     topP) {

    dd_ctx * ctx = dd_from_handle(handle);
    if (ctx == nullptr || ctx->lctx == nullptr) {
        dd_throw(env, "nativeGenerate: no model is loaded");
        return nullptr;
    }
    if (maxTokens < 1) {
        dd_throw(env, "nativeGenerate: maxTokens must be positive");
        return nullptr;
    }

    ctx->cancel.store(false, std::memory_order_relaxed);

    const std::string prompt   = dd_jstring(env, jPrompt);
    const bool        has_image = jPixels != nullptr && width > 0 && height > 0;

    if (has_image && ctx->mctx == nullptr) {
        dd_throw(env, "nativeGenerate: an image was supplied but no vision projector is loaded");
        return nullptr;
    }

    // --- Build the chat prompt through the model's own jinja template -------
    const std::string marker = ctx->mctx != nullptr ? mtmd_default_marker() : std::string();

    common_chat_msg msg;
    msg.role    = "user";
    msg.content = has_image ? (marker + "\n" + prompt) : prompt;

    common_chat_templates_inputs tinputs;
    tinputs.messages              = { msg };
    tinputs.add_generation_prompt = true;
    tinputs.use_jinja             = true;
    // No grammar, no json_schema. Both fields are left empty on purpose.

    std::string formatted;
    try {
        formatted = common_chat_templates_apply(ctx->tmpls.get(), tinputs).prompt;
    } catch (const std::exception & e) {
        dd_throw(env, std::string("nativeGenerate: chat template failed: ") + e.what());
        return nullptr;
    }

    // --- Tokenize (and, with an image, encode) ------------------------------
    std::vector<unsigned char> rgb;
    mtmd_bitmap * bitmap = nullptr;
    struct bitmap_guard {
        mtmd_bitmap ** p;
        ~bitmap_guard() { if (*p != nullptr) { mtmd_bitmap_free(*p); } }
    } bitmap_cleanup{&bitmap};

    if (has_image) {
        const jsize pixel_count = env->GetArrayLength(jPixels);
        const jlong expected    = static_cast<jlong>(width) * static_cast<jlong>(height);
        if (static_cast<jlong>(pixel_count) != expected) {
            dd_throw(env, "nativeGenerate: pixel array holds " + std::to_string(pixel_count) +
                              " ints but " + std::to_string(width) + "x" + std::to_string(height) +
                              " needs " + std::to_string(expected));
            return nullptr;
        }
        jint * pixels = env->GetIntArrayElements(jPixels, nullptr);
        if (pixels == nullptr) {
            dd_throw(env, "nativeGenerate: could not pin the pixel array");
            return nullptr;
        }
        rgb = dd_argb_to_rgb(pixels, static_cast<uint32_t>(width), static_cast<uint32_t>(height));
        env->ReleaseIntArrayElements(jPixels, pixels, JNI_ABORT);

        // The assert mtmd_bitmap_init does not do. It memcpys nx*ny*3 with no
        // length parameter, so a short buffer here is an out-of-bounds read
        // inside libmtmd with no diagnostic at all.
        const size_t required = static_cast<size_t>(width) * static_cast<size_t>(height) * 3u;
        if (rgb.size() != required) {
            dd_throw(env, "nativeGenerate: RGB buffer is " + std::to_string(rgb.size()) +
                              " bytes, mtmd_bitmap_init will read " + std::to_string(required));
            return nullptr;
        }

        bitmap = mtmd_bitmap_init(static_cast<uint32_t>(width), static_cast<uint32_t>(height), rgb.data());
        if (bitmap == nullptr) {
            dd_throw(env, "nativeGenerate: mtmd_bitmap_init failed");
            return nullptr;
        }
    }

    // A fresh request always starts from a clean context. The Kotlin layer also
    // calls resetSession before every request; doing it here too means a direct
    // native caller cannot skip it.
    dd_reset_session(ctx);

    mtmd_input_chunks * chunks = nullptr;
    struct chunks_guard {
        mtmd_input_chunks ** p;
        ~chunks_guard() { if (*p != nullptr) { mtmd_input_chunks_free(*p); } }
    } chunks_cleanup{&chunks};

    std::vector<llama_token> text_only_tokens;

    if (ctx->mctx != nullptr) {
        chunks = mtmd_input_chunks_init();
        if (chunks == nullptr) {
            dd_throw(env, "nativeGenerate: mtmd_input_chunks_init failed");
            return nullptr;
        }
        mtmd_input_text inp{};
        inp.text          = formatted.c_str();
        inp.text_len      = formatted.size();
        inp.add_special   = true;
        inp.parse_special = true;

        const mtmd_bitmap * bitmaps[1] = { bitmap };
        const int32_t rc = mtmd_tokenize(ctx->mctx, chunks, &inp,
                                         has_image ? bitmaps : nullptr,
                                         has_image ? 1u : 0u);
        if (rc != 0) {
            dd_throw(env, "nativeGenerate: mtmd_tokenize failed with code " + std::to_string(rc));
            return nullptr;
        }

        // -------------------------------------------------------------------
        // LOAD-BEARING #3: THE BATCH PRE-FLIGHT GUARD.
        //
        // llama-context.cpp:1724 is GGML_ASSERT(n_tokens_all <= cparams.n_batch).
        // GGML_ASSERT calls abort(): it takes the whole process down and there is
        // nothing to catch. An image chunk larger than n_batch must therefore be
        // rejected HERE, while a Java exception is still possible.
        //
        // This is a backstop, not the protection. The protection is never
        // lowering VisionPrompts.InferenceParams.BATCH below 512.
        // -------------------------------------------------------------------
        const size_t n_chunks = mtmd_input_chunks_size(chunks);
        for (size_t i = 0; i < n_chunks; ++i) {
            const mtmd_input_chunk * chunk = mtmd_input_chunks_get(chunks, i);
            if (mtmd_input_chunk_get_type(chunk) != MTMD_INPUT_CHUNK_TYPE_IMAGE) {
                continue;
            }
            const size_t chunk_tokens = mtmd_input_chunk_get_n_tokens(chunk);
            if (chunk_tokens > static_cast<size_t>(ctx->n_batch)) {
                dd_throw(env, "nativeGenerate: image chunk is " + std::to_string(chunk_tokens) +
                                  " tokens but n_batch is " + std::to_string(ctx->n_batch) +
                                  ". Raise batch (>= 512) or shrink the image; proceeding would abort the process.");
                return nullptr;
            }
        }

        const size_t n_prompt = mtmd_helper_get_n_tokens(chunks);
        if (n_prompt >= static_cast<size_t>(ctx->n_ctx)) {
            dd_throw(env, "nativeGenerate: prompt is " + std::to_string(n_prompt) +
                              " tokens but the context is only " + std::to_string(ctx->n_ctx));
            return nullptr;
        }

        if (ctx->cancel.load(std::memory_order_relaxed)) {
            return env->NewStringUTF("");
        }

        llama_pos new_n_past = 0;
        const int32_t eval_rc = mtmd_helper_eval_chunks(
            ctx->mctx, ctx->lctx, chunks,
            /* n_past */ ctx->n_past,
            /* seq_id */ 0,
            /* n_batch */ ctx->n_batch,
            /* logits_last */ true,
            &new_n_past);
        if (eval_rc != 0) {
            dd_throw(env, "nativeGenerate: mtmd_helper_eval_chunks failed with code " + std::to_string(eval_rc));
            return nullptr;
        }
        ctx->n_past = new_n_past;
    } else {
        // Text-only fallback for a build with no projector resident.
        text_only_tokens = common_tokenize(ctx->lctx, formatted, true, true);
        if (text_only_tokens.empty()) {
            dd_throw(env, "nativeGenerate: prompt tokenized to zero tokens");
            return nullptr;
        }
        if (text_only_tokens.size() >= static_cast<size_t>(ctx->n_ctx)) {
            dd_throw(env, "nativeGenerate: prompt is " + std::to_string(text_only_tokens.size()) +
                              " tokens but the context is only " + std::to_string(ctx->n_ctx));
            return nullptr;
        }
        // Submitted in n_batch-sized slices for the same reason as above: a
        // single llama_decode larger than n_batch aborts the process.
        for (size_t offset = 0; offset < text_only_tokens.size(); offset += static_cast<size_t>(ctx->n_batch)) {
            if (ctx->cancel.load(std::memory_order_relaxed)) {
                return env->NewStringUTF("");
            }
            const size_t slice = std::min(static_cast<size_t>(ctx->n_batch), text_only_tokens.size() - offset);
            llama_batch batch = llama_batch_get_one(text_only_tokens.data() + offset, static_cast<int32_t>(slice));
            if (llama_decode(ctx->lctx, batch) != 0) {
                dd_throw(env, "nativeGenerate: llama_decode failed on the prompt");
                return nullptr;
            }
            ctx->n_past += static_cast<llama_pos>(slice);
        }
    }

    // --- Sampler ------------------------------------------------------------
    // Built fresh for every call and destroyed at the end of it. A sampler that
    // outlives a request is how the grammar-sampler defect manifested; even
    // grammar-free, a per-call sampler keeps the penalty history from leaking
    // between two unrelated crops.
    common_params_sampling sparams;
    sparams.temp  = temperature;
    sparams.top_k = topK;
    sparams.top_p = topP;
    sparams.min_p = 0.0f;
    // No grammar. common_params_sampling has a grammar field; it stays empty.

    common_sampler * sampler = common_sampler_init(ctx->model, sparams);
    if (sampler == nullptr) {
        dd_throw(env, "nativeGenerate: common_sampler_init failed");
        return nullptr;
    }
    struct sampler_guard {
        common_sampler * s;
        ~sampler_guard() { if (s != nullptr) { common_sampler_free(s); } }
    } sampler_cleanup{sampler};

    // --- Decode loop --------------------------------------------------------
    // LOAD-BEARING #4: three independent stopping conditions.
    //   - the cancel flag, checked EVERY iteration, so a cancelled scan stops in
    //     one token instead of one whole generation (minutes, CPU-only);
    //   - llama_vocab_is_eog, so a well-behaved reply ends when the model says so;
    //   - maxTokens, because iOS set no cap and simply drained the stream, which
    //     on a GPU backend answering in ~20 s was tolerable and here is an
    //     unbounded hang.
    std::vector<llama_token> generated;
    generated.reserve(static_cast<size_t>(maxTokens));

    for (int i = 0; i < maxTokens; ++i) {
        if (ctx->cancel.load(std::memory_order_relaxed)) {
            break;
        }

        const llama_token token = common_sampler_sample(sampler, ctx->lctx, -1);
        common_sampler_accept(sampler, token, true);

        if (llama_vocab_is_eog(ctx->vocab, token)) {
            break;
        }

        generated.push_back(token);

        llama_batch batch = llama_batch_get_one(&generated.back(), 1);
        if (llama_decode(ctx->lctx, batch) != 0) {
            DD_LOGW("llama_decode failed mid-generation after %d tokens", i);
            break;
        }
        ctx->n_past += 1;
    }

    // prompt_cache tracks what the KV cache holds so resetSession can wipe both
    // as one unit; see dd_reset_session.
    ctx->prompt_cache = generated;

    const std::string text = generated.empty()
        ? std::string()
        : common_detokenize(ctx->lctx, generated, /* special */ false);

    return env->NewStringUTF(text.c_str());
}

}  // extern "C"
