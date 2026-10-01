using System.Runtime.InteropServices;

namespace Nexus.Core;

internal static partial class NxNative
{
    [LibraryImport("nexuscore", StringMarshalling = StringMarshalling.Utf8)]
    internal static partial int nx_init(string configJson);

    [LibraryImport("nexuscore", StringMarshalling = StringMarshalling.Utf8)]
    internal static partial nint nx_call(string method, string requestJson);

    [LibraryImport("nexuscore")]
    internal static partial void nx_free(nint ptr);

    [LibraryImport("nexuscore")]
    internal static partial int nx_shutdown();

    [LibraryImport("nexuscore")]
    internal static unsafe partial int nx_subscribe(delegate* unmanaged[Cdecl]<nint, void> callback);
}
