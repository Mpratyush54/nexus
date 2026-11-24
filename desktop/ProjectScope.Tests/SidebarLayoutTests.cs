using Nexus.Core;
using Xunit;

public class SidebarLayoutTests
{
    [Theory]
    [InlineData(820, 64, 160, 572)]
    [InlineData(540, 96, 160, 260)]
    [InlineData(260, 100, 160, 0)]
    [InlineData(820, 64, 240, 492)]
    public void ChatListLeavesRoomForEveryFooterItem(double viewport, double top, double footer, double expected)
    {
        Assert.Equal(expected, SidebarLayout.ChatHeight(viewport, top, footer));
    }

    [Fact]
    public void UnmeasuredLayoutCannotProduceAnInvalidHeight()
    {
        Assert.Equal(0, SidebarLayout.ChatHeight(double.NaN, 0, 160));
        Assert.Equal(0, SidebarLayout.ChatHeight(820, double.PositiveInfinity, 160));
    }
}
