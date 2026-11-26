// NexusDictation.m - voice input (dictation) for the macOS head, part of
// libnexusmac.dylib (built together with NexusMac.m; see scripts/macos/build-app.sh).
//
// SFSpeechRecognizer fed by an AVAudioEngine input tap, the way Apple's
// SpokenWord sample does it:
//   - Speech Recognition and Microphone permission are asked for on first use
//     (Info.plist: NSSpeechRecognitionUsageDescription, NSMicrophoneUsageDescription;
//     the hardened runtime also needs com.apple.security.device.audio-input);
//   - recognition stays on this Mac when the recognizer supports it
//     (requiresOnDeviceRecognition), with punctuation on macOS 13+;
//   - partial results stream back as the words are heard, and the input level
//     drives the level bars.
// Audio buffers go only to the recognizer; nothing is written or kept.
//
// C ABI (desktop/uno/Platform/MacDictation.cs):
//   nx_mac_dictation_start(locale, callback)  -> 1 when the attempt began
//   nx_mac_dictation_stop()                    -> ends audio; the final result follows
//   nx_mac_dictation_cancel()                  -> stops at once
//   nx_mac_dictation_locales(buf, len)         -> comma-separated supported locales
// Callback(kind, text, value): 1 interim text, 2 final text, 3 level (value 0..1),
// 4 ended (value = problem code), 5 listening.

#import <AVFoundation/AVFoundation.h>
#include <math.h>
#include <time.h>
#import <Foundation/Foundation.h>
#import <Speech/Speech.h>

typedef void (*nx_dictation_fn)(int32_t kind, const char *text, float value);

// Keep in sync with Nexus.Core.DictationProblem.
enum {
    NXDictOk = 0,
    NXDictMicDenied = 1,
    NXDictSpeechDenied = 2,
    NXDictNoRecognizer = 4,
    NXDictNoMicrophone = 5,
    NXDictFailed = 6,
};

enum {
    NXDictInterim = 1,
    NXDictFinal = 2,
    NXDictLevel = 3,
    NXDictEnded = 4,
    NXDictListening = 5,
};

static nx_dictation_fn s_dictation;
static SFSpeechRecognizer *s_recognizer;
static SFSpeechAudioBufferRecognitionRequest *s_request;
static SFSpeechRecognitionTask *s_task;
static AVAudioEngine *s_engine;
static uint64_t s_generation;
static BOOL s_endSent;

static void nx_dict_emit(uint64_t generation, int32_t kind, NSString *text, float value)
{
    if (generation != s_generation || s_dictation == NULL) return;
    s_dictation(kind, text != nil ? text.UTF8String : "", value);
}

static void nx_dict_main(dispatch_block_t block)
{
    if ([NSThread isMainThread]) block();
    else dispatch_async(dispatch_get_main_queue(), block);
}

// Stops the audio engine and drops the request (main thread).
static void nx_dict_teardown(void)
{
    if (s_engine != nil) {
        [s_engine stop];
        [s_engine.inputNode removeTapOnBus:0];
        s_engine = nil;
    }
    [s_request endAudio];
    s_request = nil;
    s_task = nil;
    s_recognizer = nil;
}

static void nx_dict_end(uint64_t generation, int32_t problem)
{
    if (generation != s_generation || s_endSent) return;
    s_endSent = YES;
    nx_dict_teardown();
    nx_dict_emit(generation, NXDictEnded, nil, (float)problem);
}

static void nx_dict_begin(uint64_t generation, NSString *localeId)
{
    if (generation != s_generation) return;
    NSLocale *locale = localeId.length > 0 ? [NSLocale localeWithLocaleIdentifier:localeId] : [NSLocale currentLocale];
    SFSpeechRecognizer *recognizer = [[SFSpeechRecognizer alloc] initWithLocale:locale];
    if (recognizer == nil || !recognizer.isAvailable) {
        NSLog(@"[nexus-mac] dictation: no recognizer for %@", locale.localeIdentifier);
        nx_dict_end(generation, NXDictNoRecognizer);
        return;
    }
    SFSpeechAudioBufferRecognitionRequest *request = [[SFSpeechAudioBufferRecognitionRequest alloc] init];
    request.shouldReportPartialResults = YES;
    request.taskHint = SFSpeechRecognitionTaskHintDictation;
    if (recognizer.supportsOnDeviceRecognition) request.requiresOnDeviceRecognition = YES;
    if (@available(macOS 13.0, *)) request.addsPunctuation = YES;

    AVAudioEngine *engine = [[AVAudioEngine alloc] init];
    AVAudioInputNode *input = engine.inputNode;
    AVAudioFormat *format = [input outputFormatForBus:0];
    if (format.sampleRate <= 0 || format.channelCount == 0) {
        nx_dict_end(generation, NXDictNoMicrophone);
        return;
    }
    s_recognizer = recognizer;
    s_request = request;
    s_engine = engine;
    __block uint64_t lastLevel = 0;
    [input installTapOnBus:0 bufferSize:1024 format:format block:^(AVAudioPCMBuffer *buffer, AVAudioTime *when) {
        [request appendAudioPCMBuffer:buffer];
        // RMS of the first channel, about 20 times a second, for the level bars.
        uint64_t now = clock_gettime_nsec_np(CLOCK_UPTIME_RAW);
        if (now - lastLevel < 50000000ULL || buffer.floatChannelData == NULL) return;
        lastLevel = now;
        const float *samples = buffer.floatChannelData[0];
        AVAudioFrameCount n = buffer.frameLength;
        double sum = 0;
        for (AVAudioFrameCount i = 0; i < n; i++) sum += (double)samples[i] * samples[i];
        double rms = n > 0 ? sqrt(sum / n) : 0;
        // -50 dB .. -10 dB onto 0..1.
        double db = rms > 0 ? 20 * log10(rms) : -100;
        float level = (float)fmin(1.0, fmax(0.0, (db + 50) / 40));
        dispatch_async(dispatch_get_main_queue(), ^{ nx_dict_emit(generation, NXDictLevel, nil, level); });
    }];
    [engine prepare];
    NSError *error = nil;
    if (![engine startAndReturnError:&error]) {
        NSLog(@"[nexus-mac] dictation: audio engine failed: %@", error.localizedDescription);
        nx_dict_end(generation, NXDictNoMicrophone);
        return;
    }
    s_task = [recognizer recognitionTaskWithRequest:request resultHandler:^(SFSpeechRecognitionResult *result, NSError *taskError) {
        NSString *text = result.bestTranscription.formattedString;
        BOOL final = result.isFinal;
        dispatch_async(dispatch_get_main_queue(), ^{
            if (text.length > 0) nx_dict_emit(generation, final ? NXDictFinal : NXDictInterim, text, 0);
            if (final) { nx_dict_end(generation, NXDictOk); return; }
            if (taskError != nil) {
                // "No speech detected" and a cancelled task are normal ends;
                // the app says "Didn't catch that" when no words came.
                NSLog(@"[nexus-mac] dictation ended: %@ %ld", taskError.domain, (long)taskError.code);
                nx_dict_end(generation, NXDictOk);
            }
        });
    }];
    NSLog(@"[nexus-mac] dictation listening (%@, on-device=%d)", locale.localeIdentifier, request.requiresOnDeviceRecognition ? 1 : 0);
    nx_dict_emit(generation, NXDictListening, nil, 0);
}

static void nx_dict_microphone(uint64_t generation, NSString *localeId)
{
    AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
    if (status == AVAuthorizationStatusAuthorized) { nx_dict_begin(generation, localeId); return; }
    if (status != AVAuthorizationStatusNotDetermined) { nx_dict_end(generation, NXDictMicDenied); return; }
    [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {
        dispatch_async(dispatch_get_main_queue(), ^{
            if (granted) nx_dict_begin(generation, localeId);
            else nx_dict_end(generation, NXDictMicDenied);
        });
    }];
}

__attribute__((visibility("default")))
int32_t nx_mac_dictation_start(const char *locale, nx_dictation_fn callback)
{
    NSString *localeId = locale != NULL ? [NSString stringWithUTF8String:locale] : @"";
    nx_dict_main(^{
        // A new attempt replaces any running one.
        s_generation++;
        nx_dict_teardown();
        s_endSent = NO;
        s_dictation = callback;
        uint64_t generation = s_generation;
        [SFSpeechRecognizer requestAuthorization:^(SFSpeechRecognizerAuthorizationStatus status) {
            dispatch_async(dispatch_get_main_queue(), ^{
                if (status != SFSpeechRecognizerAuthorizationStatusAuthorized) {
                    nx_dict_end(generation, NXDictSpeechDenied);
                    return;
                }
                nx_dict_microphone(generation, localeId);
            });
        }];
    });
    return 1;
}

__attribute__((visibility("default")))
void nx_mac_dictation_stop(void)
{
    nx_dict_main(^{
        uint64_t generation = s_generation;
        if (s_engine != nil) {
            [s_engine stop];
            [s_engine.inputNode removeTapOnBus:0];
            s_engine = nil;
        }
        if (s_request != nil) {
            [s_request endAudio];
            // The final result normally follows within a moment; never wait long.
            dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(2 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
                [s_task cancel];
                nx_dict_end(generation, NXDictOk);
            });
        } else {
            nx_dict_end(generation, NXDictOk);
        }
    });
}

__attribute__((visibility("default")))
void nx_mac_dictation_cancel(void)
{
    nx_dict_main(^{
        [s_task cancel];
        nx_dict_end(s_generation, NXDictOk);
    });
}

__attribute__((visibility("default")))
int32_t nx_mac_dictation_locales(char *out, int32_t len)
{
    if (out == NULL || len <= 0) return 0;
    out[0] = 0;
    @autoreleasepool {
        NSMutableArray<NSString *> *ids = [NSMutableArray array];
        for (NSLocale *locale in [SFSpeechRecognizer supportedLocales]) {
            // en-US, not en_US.
            [ids addObject:[locale.localeIdentifier stringByReplacingOccurrencesOfString:@"_" withString:@"-"]];
        }
        [ids sortUsingSelector:@selector(compare:)];
        NSString *joined = [ids componentsJoinedByString:@","];
        strlcpy(out, joined.UTF8String, (size_t)len);
        return (int32_t)ids.count;
    }
}
