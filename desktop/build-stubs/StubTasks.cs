using System;
using Microsoft.Build.Framework;
using MSBTask = Microsoft.Build.Utilities.Task;

namespace Microsoft.Build.AppxPackage
{
    public abstract class StubTaskBase : MSBTask
    {
        public string VsTelemetrySession { get; set; }
        public string ProjectName { get; set; }
        public string Platform { get; set; }
        public string Configuration { get; set; }
        public string Condition { get; set; }
        public ITaskItem[] Inputs { get; set; }
    }

    public class RemovePayloadDuplicates : StubTaskBase
    {
        [Output] public ITaskItem[] Filtered { get; set; }
        public override bool Execute()
        {
            Filtered = Inputs ?? Array.Empty<ITaskItem>();
            return true;
        }
    }

    public class GetSdkFileFullPath : StubTaskBase
    {
        public string FileArchitecture { get; set; }
        public string FileName { get; set; }
        public string FullFilePath { get; set; }
        public string MSBuildExtensionsPath64Exists { get; set; }
        public string RequireExeExtension { get; set; }
        public string SDKIdentifier { get; set; }
        public string SDKVersion { get; set; }
        public string TargetPlatformIdentifier { get; set; }
        public string TargetPlatformMinVersion { get; set; }
        public string TargetPlatformSdkRootOverride { get; set; }
        public string TargetPlatformVersion { get; set; }
        [Output] public string ActualFullFilePath { get; set; }
        [Output] public string ActualFileArchitecture { get; set; }
        public override bool Execute()
        {
            if (!string.IsNullOrEmpty(FileName) && FileName.Equals("MakePri.exe", StringComparison.OrdinalIgnoreCase))
            {
                foreach (var r in new[]
                {
                    @"C:\Program Files (x86)\Windows Kits\10\bin\10.0.22621.0\x64",
                    @"C:\Program Files (x86)\Windows Kits\10\bin\x64"
                })
                {
                    var p = System.IO.Path.Combine(r, "MakePri.exe");
                    if (System.IO.File.Exists(p))
                    {
                        ActualFullFilePath = p;
                        ActualFileArchitecture = "x64";
                        return true;
                    }
                }
            }
            ActualFullFilePath = FullFilePath ?? "";
            ActualFileArchitecture = FileArchitecture ?? "x64";
            return true;
        }
    }

    public class GetSdkPropertyValue : StubTaskBase
    {
        public string PropertyName { get; set; }
        public string SDKIdentifier { get; set; }
        public string SDKVersion { get; set; }
        public string TargetPlatformIdentifier { get; set; }
        public string TargetPlatformMinVersion { get; set; }
        public string TargetPlatformSdkRootOverride { get; set; }
        public string TargetPlatformVersion { get; set; }
        [Output] public string PropertyValue { get; set; }
        public override bool Execute()
        {
            PropertyValue = "";
            return true;
        }
    }

    public class ExpandPayloadDirectories : StubTaskBase
    {
        public ITaskItem[] TargetDirsToExclude { get; set; }
        public ITaskItem[] TargetFilesToExclude { get; set; }
        [Output] public ITaskItem[] Expanded { get; set; }
        public override bool Execute()
        {
            Expanded = Inputs ?? Array.Empty<ITaskItem>();
            return true;
        }
    }

    public class GetDefaultResourceLanguage : StubTaskBase
    {
        public string DefaultLanguage { get; set; }
        public string SourceAppxManifest { get; set; }
        [Output] public string DefaultResourceLanguage { get; set; }
        public override bool Execute()
        {
            DefaultResourceLanguage = string.IsNullOrEmpty(DefaultLanguage) ? "en-US" : DefaultLanguage;
            return true;
        }
    }

    public class GetPackageArchitecture : StubTaskBase
    {
        public string ProjectArchitecture { get; set; }
        public string RecursiveProjectArchitecture { get; set; }
        [Output] public string PackageArchitecture { get; set; }
        public override bool Execute()
        {
            PackageArchitecture = string.IsNullOrEmpty(ProjectArchitecture) ? "x64" : ProjectArchitecture;
            return true;
        }
    }

    public class RemoveRedundantXamlFilesFromSdkPayload : StubTaskBase
    {
        [Output] public ITaskItem[] Filtered { get; set; }
        public override bool Execute()
        {
            Filtered = Inputs ?? Array.Empty<ITaskItem>();
            return true;
        }
    }

    public class ValidateConfiguration : StubTaskBase
    {
        public string ProjectLanguage { get; set; }
        public string TargetPlatformIdentifier { get; set; }
        public string TargetPlatformMinVersion { get; set; }
        public string TargetPlatformVersion { get; set; }
        public override bool Execute() => true;
    }
}

namespace Microsoft.Build.Packaging.Pri.Tasks
{
    public abstract class StubTaskBase : MSBTask
    {
        public string VsTelemetrySession { get; set; }
        public string ProjectName { get; set; }
        public string Platform { get; set; }
        public string Condition { get; set; }
        public ITaskItem[] Inputs { get; set; }
        public string MakePriExeFullPath { get; set; }
        public string MakePriExtensionPath { get; set; }
        public string IntermediateDirectory { get; set; }
        public string IntermediateExtension { get; set; }
        public string AdditionalMakepriExeParameters { get; set; }
        public string ExcludeXamlFromLibraryLayoutsWhenXbfIsPresent { get; set; }
        public string TargetPlatformIdentifier { get; set; }
        public string TargetPlatformVersion { get; set; }
        public string DefaultResourceLanguage { get; set; }
        public string DefaultResourceQualifiers { get; set; }
    }

    public class ExpandPriContent : StubTaskBase
    {
        [Output] public ITaskItem[] Expanded { get; set; }
        [Output] public ITaskItem[] IntermediateFileWrites { get; set; }
        public override bool Execute()
        {
            Expanded = Array.Empty<ITaskItem>();
            IntermediateFileWrites = Array.Empty<ITaskItem>();
            return true;
        }
    }

    public class GenerateProjectPriFile : StubTaskBase
    {
        public string AppxBundleAutoResourcePackageQualifiers { get; set; }
        public ITaskItem[] IndexFilesForQualifiersCollection { get; set; }
        public string InsertReverseMap { get; set; }
        public string MultipleQualifiersPerDimensionFoundPath { get; set; }
        public string OutputFileName { get; set; }
        public string PriConfigXmlPath { get; set; }
        public string ProjectDirectory { get; set; }
        public string ProjectPriIndexName { get; set; }
        public string QualifiersPath { get; set; }
        public override bool Execute() => true;
    }

    public class GeneratePriConfigurationFiles : StubTaskBase
    {
        public string EmbedFileResfilePath { get; set; }
        public ITaskItem[] EmbedFiles { get; set; }
        public string ExcludedLayoutResfilesPath { get; set; }
        public string FilteredLayoutResfilesPath { get; set; }
        public ITaskItem[] LayoutFiles { get; set; }
        public ITaskItem[] PriFiles { get; set; }
        public string PriResfilesPath { get; set; }
        public ITaskItem[] PRIResourceFiles { get; set; }
        public string ResourcesResfilesPath { get; set; }
        public string UnfilteredLayoutResfilesPath { get; set; }
        public ITaskItem[] UnprocessedResourceFiles_OtherLanguages { get; set; }
        [Output] public ITaskItem[] AdditionalResourceResFiles { get; set; }
        public override bool Execute()
        {
            AdditionalResourceResFiles = Array.Empty<ITaskItem>();
            return true;
        }
    }

    public class GenerateMainPriConfigurationFile : StubTaskBase
    {
        public override bool Execute() => true;
    }

    public class CreatePriConfigXmlForSplitting : StubTaskBase
    {
        public override bool Execute() => true;
    }

    public class CreatePriConfigXmlForMainPackageFileMap : StubTaskBase
    {
        public override bool Execute() => true;
    }

    public class CreatePriConfigXmlForFullIndex : StubTaskBase
    {
        public ITaskItem[] AdditionalResourceResFiles { get; set; }
        public string EmbedFileResfilePath { get; set; }
        public string LayoutResfilesPath { get; set; }
        public string PriConfigXmlDefaultSnippetPath { get; set; }
        public string PriConfigXmlPath { get; set; }
        public string PriInitialPath { get; set; }
        public string PriResfilesPath { get; set; }
        public string ResourcesResfilesPath { get; set; }
        public override bool Execute() => true;
    }

    public class CreatePriFilesForPortableLibraries : StubTaskBase
    {
        public string AppxBundleAutoResourcePackageQualifiers { get; set; }
        public ITaskItem[] ContentToIndex { get; set; }
        public string SkipIntermediatePriGenerationForResourceFiles { get; set; }
        [Output] public ITaskItem[] CreatedPriFiles { get; set; }
        [Output] public ITaskItem[] IntermediateFileWrites { get; set; }
        [Output] public ITaskItem[] UnprocessedReswFiles_DefaultLanguage { get; set; }
        [Output] public ITaskItem[] UnprocessedReswFiles_OtherLanguages { get; set; }
        public override bool Execute()
        {
            CreatedPriFiles = Array.Empty<ITaskItem>();
            IntermediateFileWrites = Array.Empty<ITaskItem>();
            UnprocessedReswFiles_DefaultLanguage = Array.Empty<ITaskItem>();
            UnprocessedReswFiles_OtherLanguages = Array.Empty<ITaskItem>();
            return true;
        }
    }

    public class RemoveDuplicatePriFiles : StubTaskBase
    {
        [Output] public ITaskItem[] Filtered { get; set; }
        public override bool Execute()
        {
            Filtered = Inputs ?? Array.Empty<ITaskItem>();
            return true;
        }
    }

    public class UpdateMainPackageFileMap : StubTaskBase
    {
        public override bool Execute() => true;
    }
}
